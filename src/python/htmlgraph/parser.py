"""
HTML parser wrapper using justhtml.

Provides CSS selector-based querying and data extraction from HTML files.
"""

import re
from datetime import datetime
from pathlib import Path
from typing import Any

from justhtml import JustHTML


class HtmlParser:
    """
    Parser for HtmlGraph HTML files using justhtml.

    Provides:
    - CSS selector queries
    - Data attribute extraction
    - Graph structure parsing (nodes, edges)
    """

    def __init__(
        self, html_content: str | None = None, filepath: Path | str | None = None
    ):
        """
        Initialize parser with HTML content or file.

        Args:
            html_content: Raw HTML string
            filepath: Path to HTML file (will read content)
        """
        if filepath:
            filepath = Path(filepath)
            html_content = filepath.read_text(encoding="utf-8")

        if not html_content:
            raise ValueError("Either html_content or filepath must be provided")

        # safe=False: prevent justhtml>=1.x from sanitizing the <article> element.
        # justhtml>=1.0 enabled HTML sanitization by default, which strips <article>
        # since it's not on the default allowlist. HtmlGraph uses <article> as the
        # root node element (with id/data-* attributes), so sanitization must be
        # disabled to preserve all node metadata. Without this, all node collections
        # return empty (bug-8aec5198).
        self.html = JustHTML(html_content, safe=False)
        self._raw = html_content

    @classmethod
    def from_file(cls, filepath: Path | str) -> "HtmlParser":
        """Create parser from file path."""
        return cls(filepath=filepath)

    @classmethod
    def from_string(cls, html_content: str) -> "HtmlParser":
        """Create parser from HTML string."""
        return cls(html_content=html_content)

    def query(self, selector: str) -> list[Any]:
        """
        Query elements using CSS selector.

        Args:
            selector: CSS selector string

        Returns:
            List of matching elements
        """
        result: list[Any] = self.html.query(selector)
        return result

    def query_one(self, selector: str) -> Any | None:
        """
        Query single element using CSS selector.

        Args:
            selector: CSS selector string

        Returns:
            First matching element or None
        """
        results = self.html.query(selector)
        return results[0] if results else None

    def get_article(self) -> Any | None:
        """Get the main article element (graph node root)."""
        results = self.query("article[id]")
        return results[0] if results else None

    def get_node_id(self) -> str | None:
        """Extract node ID from article element."""
        article = self.get_article()
        if article:
            result: str | None = article.attrs.get("id")
            return result
        return None

    def get_data_attribute(self, element: Any, attr: str) -> str | None:
        """Get a data-* attribute value from an element."""
        if element is None:
            return None
        result: str | None = element.attrs.get(f"data-{attr}")
        return result

    def get_all_data_attributes(self, element: Any) -> dict[str, str]:
        """Get all data-* attributes from an element."""
        if not element:
            return {}

        attrs = {}
        for key, value in element.attrs.items():
            if key.startswith("data-"):
                attr_name = key[5:]  # Remove 'data-' prefix
                attrs[attr_name] = value
        return attrs

    def get_node_metadata(self) -> dict[str, Any]:
        """
        Extract all metadata from the node article.

        Returns dict with:
        - id, type, status, priority
        - created, updated timestamps
        - agent_assigned
        - Any custom data-* attributes
        """
        article = self.get_article()
        if not article:
            return {}

        metadata = {
            "id": article.attrs.get("id"),
        }

        # Standard attributes
        for attr in [
            "type",
            "status",
            "priority",
            "agent-assigned",
            "track-id",
            "plan-task-id",
            "claimed-by-session",
            "spike-subtype",
            "session-id",
            "from-feature-id",
            "to-feature-id",
            "model-name",
        ]:
            value = self.get_data_attribute(article, attr)
            if value:
                key = attr.replace("-", "_")
                metadata[key] = value

        # Boolean attributes
        auto_generated = self.get_data_attribute(article, "auto-generated")
        if auto_generated:
            metadata["auto_generated"] = auto_generated.lower() == "true"

        # Pattern sequence (for pattern nodes)
        sequence_attr = self.get_data_attribute(article, "sequence")
        if sequence_attr:
            try:
                import json

                metadata["sequence"] = json.loads(sequence_attr)
            except (json.JSONDecodeError, ValueError):
                # Invalid JSON, skip
                pass

        # Timestamps (with fallbacks for session-specific attributes)
        claimed_at = self.get_data_attribute(article, "claimed-at")
        if claimed_at:
            try:
                metadata["claimed_at"] = datetime.fromisoformat(
                    claimed_at.replace("Z", "+00:00")
                )
            except ValueError:
                metadata["claimed_at"] = claimed_at

        created_value = self.get_data_attribute(
            article, "created"
        ) or self.get_data_attribute(article, "started-at")
        if created_value:
            try:
                metadata["created"] = datetime.fromisoformat(
                    created_value.replace("Z", "+00:00")
                )
            except ValueError:
                metadata["created"] = created_value

        updated_value = self.get_data_attribute(
            article, "updated"
        ) or self.get_data_attribute(article, "last-activity")
        if updated_value:
            try:
                metadata["updated"] = datetime.fromisoformat(
                    updated_value.replace("Z", "+00:00")
                )
            except ValueError:
                metadata["updated"] = updated_value

        return metadata

    def get_title(self) -> str | None:
        """Get node title from h1 or title element."""
        # Try h1 in header first
        h1_results = self.query("article header h1")
        h1 = h1_results[0] if h1_results else None
        if h1:
            text: str = h1.to_text().strip()
            return text

        # Fall back to title element
        title_results = self.query("title")
        title = title_results[0] if title_results else None
        if title:
            text2: str = title.to_text().strip()
            return text2

        return None

    def get_edges(self) -> dict[str, list[dict[str, Any]]]:
        """
        Extract all graph edges from nav[data-graph-edges].

        Returns dict keyed by relationship type, with list of edge dicts:
        {
            "blocks": [{"target_id": "...", "title": "...", ...}],
            "related": [...],
        }
        """
        edges: dict[str, list[dict[str, Any]]] = {}

        edge_nav_results = self.query("nav[data-graph-edges]")
        edge_nav = edge_nav_results[0] if edge_nav_results else None
        if not edge_nav:
            return edges

        # Find all edge sections
        sections = self.query("nav[data-graph-edges] section[data-edge-type]")
        for section in sections:
            rel_type = section.attrs.get("data-edge-type", "related")
            edges[rel_type] = []

            # Find all links in this section
            links = section.query("a[href]")
            for link in links:
                href = link.attrs.get("href", "")

                # Extract target ID from href
                target_id = href
                if href.endswith(".html"):
                    target_id = href[:-5]  # Remove .html
                if "/" in target_id:
                    target_id = target_id.split("/")[-1]  # Get filename only

                edge_data = {
                    "target_id": target_id,
                    "title": link.to_text().strip() if link else None,
                    "relationship": link.attrs.get("data-relationship", rel_type),
                }

                # Get additional data attributes
                since = link.attrs.get("data-since")
                if since:
                    try:
                        edge_data["since"] = datetime.fromisoformat(
                            since.replace("Z", "+00:00")
                        )
                    except ValueError:
                        edge_data["since"] = since

                # Any other data attributes as properties
                for key, value in link.attrs.items():
                    if key.startswith("data-") and key not in [
                        "data-relationship",
                        "data-since",
                    ]:
                        if "properties" not in edge_data:
                            edge_data["properties"] = {}
                        edge_data["properties"][key[5:]] = value

                edges[rel_type].append(edge_data)

        return edges

    def get_steps(self) -> list[dict[str, Any]]:
        """
        Extract implementation steps from section[data-steps].

        Returns list of step dicts:
        [{"description": "...", "completed": bool, "agent": "..."}]
        """
        steps = []

        step_items = self.query("section[data-steps] ol li")
        for item in step_items:
            completed = item.attrs.get("data-completed", "false").lower() == "true"
            agent = item.attrs.get("data-agent")
            step_id = item.attrs.get("data-step-id")

            # Extract description (remove emoji prefix if present)
            text = item.to_text().strip() if item else ""
            # Remove common status emojis
            text = re.sub(r"^[✅⏳❌🔄]\s*", "", text)

            depends_on_raw = item.attrs.get("data-depends-on", "")
            depends_on = (
                [d.strip() for d in depends_on_raw.split(",") if d.strip()]
                if depends_on_raw
                else []
            )

            step_dict: dict[str, Any] = {
                "description": text,
                "completed": completed,
                "agent": agent,
            }
            if step_id:
                step_dict["step_id"] = step_id
            if depends_on:
                step_dict["depends_on"] = depends_on

            steps.append(step_dict)

        return steps

    def get_properties(self) -> dict[str, Any]:
        """
        Extract properties from section[data-properties].

        Returns dict of property key-value pairs.
        """
        properties = {}

        prop_items = self.query("section[data-properties] dd[data-key]")
        for item in prop_items:
            key = item.attrs.get("data-key")
            if not key:
                continue

            value = item.attrs.get("data-value")
            unit = item.attrs.get("data-unit")

            # Try to parse numeric values
            if value:
                try:
                    if "." in value:
                        value = float(value)
                    else:
                        value = int(value)
                except (ValueError, TypeError):
                    pass

            if unit:
                properties[key] = {"value": value, "unit": unit}
            else:
                properties[key] = value

        # Extract session-specific data attributes from article element
        article = self.get_article()
        if article and self.get_data_attribute(article, "type") == "session":
            # Add event_count if present
            event_count_str: str | None = article.attrs.get("data-event-count")
            if event_count_str:
                try:
                    properties["event_count"] = int(event_count_str)  # type: ignore[assignment]
                except (ValueError, TypeError):
                    pass

            # Add agent if present
            agent = article.attrs.get("data-agent")
            if agent:
                properties["agent"] = agent

            # Add transcript_id if present (for Claude Code transcript integration)
            transcript_id = article.attrs.get("data-transcript-id")
            if transcript_id:
                properties["transcript_id"] = transcript_id

        return properties

    def get_content(self) -> str:
        """Extract main content from section[data-content]."""
        content_section_results = self.query("section[data-content]")
        content_section = (
            content_section_results[0] if content_section_results else None
        )
        if not content_section:
            return ""

        # Get text content excluding the h3 header
        text_parts = []
        for child in content_section.children:
            if hasattr(child, "name") and child.name == "h3":
                continue
            if hasattr(child, "to_text"):
                text = child.to_text().strip()
                if text:
                    text_parts.append(text)

        return "\n".join(text_parts)

    def get_findings(self) -> str | None:
        """Extract findings from section[data-findings] (Spike-specific)."""
        findings_section_results = self.query("section[data-findings]")
        findings_section = (
            findings_section_results[0] if findings_section_results else None
        )
        if not findings_section:
            return None

        # Look for findings-content div using full selector
        content_div_results = self.query("section[data-findings] div.findings-content")
        content_div = content_div_results[0] if content_div_results else None
        if content_div:
            text = content_div.to_text().strip()
            return text if text else None

        # Fallback: get all text excluding h3 header
        text_parts = []
        for child in findings_section.children:
            if hasattr(child, "name") and child.name == "h3":
                continue
            if hasattr(child, "to_text"):
                text = child.to_text().strip()
                if text:
                    text_parts.append(text)

        result = "\n".join(text_parts)
        return result if result else None

    def get_decision(self) -> str | None:
        """Extract decision from section[data-decision] (Spike-specific)."""
        decision_section_results = self.query("section[data-decision]")
        decision_section = (
            decision_section_results[0] if decision_section_results else None
        )
        if not decision_section:
            return None

        # Get text content excluding the h3 header
        text_parts = []
        for child in decision_section.children:
            if hasattr(child, "name") and child.name == "h3":
                continue
            if hasattr(child, "to_text"):
                text = child.to_text().strip()
                if text:
                    text_parts.append(text)

        result = "\n".join(text_parts)
        return result if result else None

    def parse_full_node(self) -> dict[str, Any]:
        """
        Parse complete node data from HTML.

        Returns dict suitable for Node.from_dict().
        """
        metadata = self.get_node_metadata()
        title = self.get_title()

        result = {
            **metadata,
            "title": title or metadata.get("id", "Untitled"),
            "edges": self.get_edges(),
            "steps": self.get_steps(),
            "properties": self.get_properties(),
            "content": self.get_content(),
        }

        # Add Spike-specific fields if present
        findings = self.get_findings()
        if findings is not None:
            result["findings"] = findings

        decision = self.get_decision()
        if decision is not None:
            result["decision"] = decision

        return result

