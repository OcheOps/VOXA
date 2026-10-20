// Package summary generates meeting summaries on the server with the same
// templates, prompts, chunking and output cleanup as the Voxa desktop app
// (frontend/src-tauri/src/summary), against operator-enabled LLM providers.
package summary

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// The JSON files are copies of frontend/src-tauri/templates; a test fails if
// they drift.
//
//go:embed templates/*.json
var templateFiles embed.FS

// DefaultTemplateID is used when the workspace has no template preference
// (the desktop default).
const DefaultTemplateID = "standard_meeting"

type TemplateSection struct {
	Title             string  `json:"title"`
	Instruction       string  `json:"instruction"`
	Format            string  `json:"format"`
	ItemFormat        *string `json:"item_format,omitempty"`
	ExampleItemFormat *string `json:"example_item_format,omitempty"`
}

type Template struct {
	ID          string            `json:"-"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Sections    []TemplateSection `json:"sections"`
}

func (t Template) validate() error {
	if t.Name == "" || t.Description == "" || len(t.Sections) == 0 {
		return errors.New("template needs a name, description and sections")
	}
	for i, section := range t.Sections {
		if section.Title == "" || section.Instruction == "" {
			return fmt.Errorf("section %d is incomplete", i)
		}
		switch section.Format {
		case "paragraph", "list", "string":
		default:
			return fmt.Errorf("section %q has invalid format %q", section.Title, section.Format)
		}
	}
	return nil
}

// MarkdownStructure mirrors Template::to_markdown_structure.
func (t Template) MarkdownStructure() string {
	var b strings.Builder
	b.WriteString("# <Add Title here>\n\n")
	for _, section := range t.Sections {
		fmt.Fprintf(&b, "**%s**\n\n", section.Title)
	}
	return b.String()
}

// SectionInstructions mirrors Template::to_section_instructions.
func (t Template) SectionInstructions() string {
	var b strings.Builder
	b.WriteString("- **For the main title (`# [AI-Generated Title]`):** Analyze the entire transcript and create a concise, descriptive title for the meeting.\n")
	for _, section := range t.Sections {
		fmt.Fprintf(&b, "- **For the '%s' section:** %s.\n", section.Title, section.Instruction)
		format := section.ItemFormat
		if format == nil {
			format = section.ExampleItemFormat
		}
		if format != nil {
			fmt.Fprintf(&b, "  - Items in this section should follow the format: `%s`.\n", *format)
		}
	}
	return b.String()
}

var templates = loadTemplates()

func loadTemplates() map[string]Template {
	out := map[string]Template{}
	entries, err := fs.Glob(templateFiles, "templates/*.json")
	if err != nil {
		panic(err)
	}
	for _, name := range entries {
		data, err := templateFiles.ReadFile(name)
		if err != nil {
			panic(err)
		}
		var template Template
		if err := json.Unmarshal(data, &template); err != nil {
			panic(fmt.Sprintf("summary template %s: %v", name, err))
		}
		template.ID = strings.TrimSuffix(strings.TrimPrefix(name, "templates/"), ".json")
		if err := template.validate(); err != nil {
			panic(fmt.Sprintf("summary template %s: %v", name, err))
		}
		out[template.ID] = template
	}
	return out
}

// GetTemplate returns a built-in template by ID.
func GetTemplate(id string) (Template, bool) {
	template, ok := templates[id]
	return template, ok
}

// Templates lists the built-in templates ordered by ID.
func Templates() []Template {
	out := make([]Template, 0, len(templates))
	for _, template := range templates {
		out = append(out, template)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
