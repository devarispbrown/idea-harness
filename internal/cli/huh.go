package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/huh"
)

// interactiveNew runs a Charm Huh form to collect the idea and model, then
// starts the workflow (§64: interactive, engaging CLI).
func interactiveNew(stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}

	var view cliProvidersView
	_ = c.do("GET", "/api/v1/projects/"+projectID+"/providers", nil, &view)

	models := []string{"auto"}
	for _, p := range view.Providers {
		if p.Ready {
			for _, m := range p.Models {
				models = append(models, p.Name+"/"+m)
			}
		}
	}

	var idea string
	modelChoice := "auto"
	if view.DefaultModel != "" {
		modelChoice = view.DefaultModel
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewText().
				Title("What are you thinking about building?").
				Description("Describe the idea, feature, problem, or change. The system will explore, research, and produce a reviewed PRD.").
				Value(&idea).
				CharLimit(4000),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Model").
				Description("Which model should drive the workflow?").
				Options(huh.NewOptions(models...)...).
				Value(&modelChoice),
		),
	).WithAccessible(false)

	if err := form.Run(); err != nil {
		return err
	}
	if strings.TrimSpace(idea) == "" {
		return usageErrorf("idea is required")
	}

	// Persist a model choice if the user changed it.
	if modelChoice != "auto" && modelChoice != view.DefaultModel {
		if err := c.do("PUT", "/api/v1/projects/"+projectID+"/providers",
			map[string]string{"default_model": modelChoice}, nil); err != nil {
			return err
		}
	}

	var out map[string]string
	if err := c.do("POST", "/api/v1/projects/"+projectID+"/initiatives",
		map[string]string{"title": idea, "description": idea}, &out); err != nil {
		return err
	}

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, ok("✓ initiative started")+" "+code(out["initiative_id"]))
	fmt.Fprintln(stdout, muted("watching progress…"))
	return watchInitiative(c, projectID, out["initiative_id"], stdout)
}
