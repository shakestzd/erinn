# /wipnote:research

Perform structured research before planning implementation. Uses WebSearch to gather best practices, patterns, and solutions from the web.

Claude performs the searches directly using WebSearch — this is not delegated to a subagent.

## Usage

```
/wipnote:research <topic> [--spike SPIKE_ID]
```

## Parameters

- `topic` (required): What you want to research (e.g., "OAuth 2.0 implementation patterns")
- `--spike` (optional): Spike ID to attach research findings to


## Examples

```bash
/wipnote:research "Real-time WebSocket best practices"
```
Research WebSocket patterns and document findings

```bash
/wipnote:research "JWT token security" --spike spike-abc123
```
Research JWT security and attach findings to existing spike


## Instructions for Claude

**CRITICAL: This command MUST be used before planning complex features.**

This command implements the mandatory research phase. Research reduces:
- Implementation mistakes (learn from others' experiences)
- Context usage (targeted research vs reading entire codebases)
- Time waste (proven patterns vs trial-and-error)

### Implementation:

**DO THIS:**

1. **Parse the research topic:**
   - Extract the main topic from user input
   - Identify 2-3 specific research questions to answer

2. **Perform web searches:**
   Use WebSearch to gather information:

   ```
   Search 1: "{topic} best practices"
   Search 2: "{topic} implementation patterns"
   Search 3: "{topic} common pitfalls"
   ```

   For each search:
   - Read top 3-5 sources
   - Extract key insights
   - Note relevant libraries/tools
   - Identify common patterns
   - Document anti-patterns to avoid

3. **Structure findings:**
   Organize research into:
   - **Key Insights:** Main takeaways (3-5 bullet points)
   - **Recommended Approach:** Preferred pattern/solution
   - **Libraries/Tools:** Specific packages to consider
   - **Anti-Patterns:** What to avoid
   - **References:** Links to sources

4. **Document research:**

   If spike provided:
   ```bash
   wipnote spike show {spike_id}
   ```
   Record findings by presenting them in the structured format below, referencing the spike ID.

   Note: spike findings are captured in the chat output. Run `wipnote spike complete <id>` when investigation is done.

   Otherwise, present findings in structured format.

5. **Validate research quality:**
   Check that findings include:
   - ✅ At least 3 different sources
   - ✅ Specific recommendations (not vague)
   - ✅ Modern practices (current year)
   - ✅ Code examples or patterns
   - ✅ Links to sources

6. **Suggest next steps:**
   - If spike exists → "Complete spike steps, then use /wipnote:plan"
   - If no spike → "Use /wipnote:plan to create track from findings"

### Research Structure Template:

```markdown
## Research: {topic}

**Completed:** {timestamp}
**Sources:** {count} references

### Key Insights
- {insight 1}
- {insight 2}
- {insight 3}
- {insight 4}
- {insight 5}

### Recommended Approach
{2-3 paragraph summary of recommended solution/pattern}

Key points:
- {specific recommendation 1}
- {specific recommendation 2}
- {specific recommendation 3}

### Libraries/Tools
- **{library_name}** - {purpose} - [link]
- **{library_name}** - {purpose} - [link]
- **{library_name}** - {purpose} - [link]

### Anti-Patterns to Avoid
- ❌ {anti-pattern 1} - {why it's bad}
- ❌ {anti-pattern 2} - {why it's bad}
- ❌ {anti-pattern 3} - {why it's bad}

### Implementation Considerations
- **Security:** {security notes}
- **Performance:** {performance notes}
- **Scalability:** {scalability notes}
- **Maintenance:** {maintenance notes}

### References
- [{source title 1}]({url1})
- [{source title 2}]({url2})
- [{source title 3}]({url3})
- [{source title 4}]({url4})
- [{source title 5}]({url5})

### Research Validated
- ✅ Multiple sources consulted
- ✅ Modern practices (current year)
- ✅ Specific recommendations provided
- ✅ Code patterns identified
- ✅ Links documented
```

### WebSearch Integration Pattern

**IMPORTANT: Use WebSearch systematically, not ad-hoc.**

Good research workflow:

1. Define specific questions:
   - "What are OAuth 2.0 best practices in 2025?"
   - "Which Python OAuth libraries are most maintained?"
   - "What are common OAuth security vulnerabilities?"

2. Use WebSearch for each question and extract key insights from top sources.

3. Synthesize findings into structured format (see Research Structure Template above).

4. Validate completeness — ensure at least 3 sources, specific recommendations, and code examples.

### Quality Checklist

Before completing research, verify:

- [ ] **Breadth:** Covered multiple aspects (implementation, security, performance)
- [ ] **Depth:** Specific recommendations, not generic advice
- [ ] **Currency:** Sources are current, not outdated
- [ ] **Practical:** Code examples or concrete patterns
- [ ] **Traceable:** Links to all sources
- [ ] **Structured:** Findings organized clearly
- [ ] **Actionable:** Can create implementation plan from research

### When to Use Research

**ALWAYS use research for:**
- ✅ New technical areas (OAuth, WebSockets, etc.)
- ✅ Security-critical features (auth, encryption)
- ✅ Performance-sensitive work (real-time, high-traffic)
- ✅ Third-party integrations (APIs, services)
- ✅ Architecture decisions (database choice, patterns)

**SKIP research for:**
- ❌ Simple CRUD operations (well-known patterns)
- ❌ Internal refactoring (codebase-specific)
- ❌ Minor UI tweaks (no external patterns needed)
- ❌ Bug fixes (debug, don't research)

### Output Format:

```
## Research Complete: {topic}

**Duration:** {time_spent}
**Sources:** {source_count}
**Quality:** ✅ Validated

### Summary
{2-3 sentence executive summary}

### Recommended Approach
{1-2 paragraphs on preferred solution}

### Key Findings
{structured findings - see template above}

### Next Steps
{context-specific recommendations}
```

### Integration with Planning Workflow

The complete planning workflow is:

```
1. /wipnote:research "{topic}"          → Gather best practices
2. /wipnote:plan "{description}" --spike → Create planning spike
3. [Complete spike steps]                  → Design solution
4. [Create track from plan]                → Structure implementation
5. [Create features from track]            → Execute work
```

**Example end-to-end:**

```bash
# Step 1: Research
/wipnote:research "OAuth 2.0 implementation with FastAPI"

# Research finds:
# - Use authlib library (most maintained)
# - Implement PKCE for security
# - Cache tokens in Redis
# - Use refresh token rotation

# Step 2: Create spike with research context
/wipnote:plan "User Authentication System" --spike --timebox 4

# Step 3: During spike, use research to:
# - Define requirements (from best practices)
# - Choose libraries (from research)
# - Design architecture (from patterns)
# - Identify risks (from anti-patterns)

# Step 4: Create track from spike
# Use: wipnote track create "title" with spike findings

# Step 5: Implement features
# With confidence from research-backed design
```

### Research-First Principle

**Why research before planning?**

1. **Avoid reinventing wheels** - Use proven patterns
2. **Learn from others' mistakes** - Avoid known pitfalls
3. **Choose right tools** - Know library landscape
4. **Design better APIs** - Follow modern conventions
5. **Estimate accurately** - Understand complexity
6. **Reduce context usage** - Targeted vs exploratory reads

**Impact on context efficiency:**

Without research:
- Read 20+ files to understand patterns → 50K tokens
- Trial-and-error implementation → 30K tokens
- Debug unknown issues → 20K tokens
- **Total: ~100K tokens**

With research:
- WebSearch 3-5 queries → 10K tokens
- Focused implementation from patterns → 20K tokens
- Fewer debugging iterations → 5K tokens
- **Total: ~35K tokens (65% savings)**

### Validation Flag

After research is complete, record it in the spike if one was provided:

```bash
wipnote spike show {spike_id}
```

Note in your response that research has been validated so that `/wipnote:plan` can incorporate findings when creating the track.
