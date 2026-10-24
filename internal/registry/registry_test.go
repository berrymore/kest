package registry_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/registry"
)

type widget struct {
	name string
	cfg  *widgetConfig
}

type widgetConfig struct {
	Size  int    `yaml:"size"`
	Color string `yaml:"color"`
}

func (c *widgetConfig) Validate() error {
	var errs []error
	if c.Size < 0 {
		errs = append(errs, &config.FieldError{Field: "size", Msg: "must not be negative"})
	}
	if c.Color == "" {
		errs = append(errs, errors.New("a color is required"))
	}
	return errors.Join(errs...)
}

func newCategory() *registry.Category[*widget] {
	c := registry.NewCategory[*widget]("widget")
	c.Register(registry.Factory[*widget]{
		Type:      "configurable",
		NewConfig: func() any { return &widgetConfig{Size: 1, Color: "red"} },
		Build: func(bc registry.BuildContext, cfg any) (*widget, error) {
			return &widget{name: bc.Name, cfg: cfg.(*widgetConfig)}, nil
		},
	})
	c.Register(registry.Factory[*widget]{
		Type: "plain",
		Build: func(bc registry.BuildContext, _ any) (*widget, error) {
			return &widget{name: bc.Name}, nil
		},
	})
	c.Register(registry.Factory[*widget]{
		Type: "broken",
		Build: func(registry.BuildContext, any) (*widget, error) {
			return nil, errors.New("cannot build")
		},
	})
	return c
}

// clusters parses a config whose clusters section is body.
func clusters(t *testing.T, body string) []config.Cluster {
	t.Helper()
	src := `version: v1
listeners:
  - name: main
    address: 127.0.0.1:8080
    http:
      virtual_hosts:
        - name: default
          domains: ["*"]
          routes: [{match: {prefix: /}, direct_response: {}}]
clusters:
` + body
	cfg, err := config.Parse([]byte(src), "kest.yaml", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg.Clusters
}

func TestRegisterPanics(t *testing.T) {
	build := func(registry.BuildContext, any) (*widget, error) { return nil, nil }
	tests := []struct {
		name string
		f    registry.Factory[*widget]
	}{
		{name: "empty type", f: registry.Factory[*widget]{Build: build}},
		{name: "no build", f: registry.Factory[*widget]{Type: "x"}},
		{name: "duplicate", f: registry.Factory[*widget]{Type: "plain", Build: build}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("Register did not panic")
				}
			}()
			newCategory().Register(tt.f)
		})
	}
}

func TestTypes(t *testing.T) {
	c := newCategory()
	if got := strings.Join(c.Types(), ","); got != "broken,configurable,plain" {
		t.Errorf("Types() = %s", got)
	}
	if _, ok := c.Lookup("plain"); !ok {
		t.Error("Lookup(plain) failed")
	}
	if c.Name() != "widget" {
		t.Errorf("Name() = %q", c.Name())
	}
}

func TestBuild(t *testing.T) {
	list := clusters(t, `  - name: a
    type: configurable
    config: {size: 3}
  - name: b
    type: configurable
  - name: c
    type: plain
`)
	c := newCategory()

	a, err := registry.Build(c, &list[0].Extension, registry.BuildContext{Name: list[0].Name})
	if err != nil {
		t.Fatalf("Build(a): %v", err)
	}
	if a.name != "a" || a.cfg.Size != 3 || a.cfg.Color != "red" {
		t.Errorf("a = %q %+v, want size 3 and the default color", a.name, a.cfg)
	}

	b, err := registry.Build(c, &list[1].Extension, registry.BuildContext{Name: list[1].Name})
	if err != nil {
		t.Fatalf("Build(b): %v", err)
	}
	if b.cfg.Size != 1 {
		t.Errorf("b config = %+v, want defaults", b.cfg)
	}

	if _, err := registry.Build(c, &list[2].Extension, registry.BuildContext{}); err != nil {
		t.Fatalf("Build(c): %v", err)
	}
}

func TestBuildErrors(t *testing.T) {
	list := clusters(t, `  - name: unknown
    type: nope
  - name: bad-field
    type: configurable
    config: {size: -1, color: "", shape: round}
  - name: invalid
    type: configurable
    config: {size: -1, color: ""}
  - name: no-config
    type: plain
    config: {x: 1}
  - name: broken
    type: broken
`)
	c := newCategory()

	tests := []struct {
		name string
		want []string
	}{
		{name: "unknown", want: []string{`kest.yaml:12:11: clusters[0].type: unknown widget type "nope" (known types: broken, configurable, plain)`}},
		{name: "bad-field", want: []string{"kest.yaml:15:35: clusters[1].config.shape: unknown field"}},
		{name: "invalid", want: []string{
			"kest.yaml:18:13: clusters[2].config: a color is required",
			"kest.yaml:18:20: clusters[2].config.size: must not be negative",
		}},
		{name: "no-config", want: []string{`kest.yaml:21:13: clusters[3].config: widget type "plain" takes no config`}},
		{name: "broken", want: []string{"kest.yaml:22:5: clusters[4]: cannot build"}},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := registry.Build(c, &list[i].Extension, registry.BuildContext{})
			if err == nil {
				t.Fatal("Build succeeded, want an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error =\n%v\nwant it to contain %q", err, w)
				}
			}
		})
	}
}

func TestBuildEmptyCategory(t *testing.T) {
	list := clusters(t, "  - {name: a, type: x}\n")
	_, err := registry.Build(registry.NewCategory[*widget]("widget"), &list[0].Extension, registry.BuildContext{})
	if err == nil || !strings.Contains(err.Error(), "none are registered") {
		t.Errorf("error = %v", err)
	}
}
