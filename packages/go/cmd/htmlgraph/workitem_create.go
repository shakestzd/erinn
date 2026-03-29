package main

import (
	"fmt"

	"github.com/shakestzd/htmlgraph/internal/models"
	"github.com/shakestzd/htmlgraph/internal/workitem"
)

func createNode(p *workitem.Project, typeName, title string, o *wiCreateOpts) (*models.Node, error) {
	switch typeName {
	case "feature":
		opts := []workitem.FeatureOption{workitem.FeatWithPriority(o.priority)}
		if o.trackID != "" {
			opts = append(opts, workitem.FeatWithTrack(o.trackID))
		}
		if o.description != "" {
			opts = append(opts, workitem.FeatWithContent(o.description))
		}
		return p.Features.Create(title, opts...)
	case "bug":
		opts := []workitem.BugOption{workitem.BugWithPriority(o.priority)}
		if o.trackID != "" {
			opts = append(opts, workitem.BugWithTrack(o.trackID))
		}
		if o.description != "" {
			opts = append(opts, workitem.BugWithContent(o.description))
		}
		return p.Bugs.Create(title, opts...)
	case "spike":
		opts := []workitem.SpikeOption{workitem.SpikeWithPriority(o.priority)}
		if o.trackID != "" {
			opts = append(opts, workitem.SpikeWithTrack(o.trackID))
		}
		return p.Spikes.Create(title, opts...)
	case "track":
		opts := []workitem.TrackOption{workitem.TrackWithPriority(o.priority)}
		if o.description != "" {
			opts = append(opts, workitem.TrackWithContent(o.description))
		}
		return p.Tracks.Create(title, opts...)
	case "plan":
		opts := []workitem.PlanOption{workitem.PlanWithPriority(o.priority)}
		if o.trackID != "" {
			opts = append(opts, workitem.PlanWithTrack(o.trackID))
		}
		if o.description != "" {
			opts = append(opts, workitem.PlanWithContent(o.description))
		}
		return p.Plans.Create(title, opts...)
	case "spec":
		opts := []workitem.SpecOption{workitem.SpecWithPriority(o.priority)}
		if o.trackID != "" {
			opts = append(opts, workitem.SpecWithTrack(o.trackID))
		}
		if o.description != "" {
			opts = append(opts, workitem.SpecWithContent(o.description))
		}
		return p.Specs.Create(title, opts...)
	default:
		return nil, fmt.Errorf("unknown type: %s", typeName)
	}
}
