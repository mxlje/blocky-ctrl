// Package config loads the list of blocky instances the UI controls.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Instances []Instance `yaml:"instances"`
}

type Instance struct {
	// ID is derived from Name and used in API paths.
	ID   string `yaml:"-"`
	Name string `yaml:"name"`
	// URL is blocky's HTTP port, e.g. http://10.0.0.2:4000. Userinfo is sent as basic auth.
	URL string `yaml:"url"`
	// ReadOnly instances are shown but not toggled, e.g. failovers or load
	// balanced replicas that get their blocking state from Redis.
	ReadOnly bool `yaml:"readonly"`
	// Label is shown on read-only instances, "readonly" by default.
	Label string `yaml:"label"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func Parse(b []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	if len(c.Instances) == 0 {
		return nil, errors.New("no instances configured")
	}
	seen := map[string]string{}
	for i := range c.Instances {
		in := &c.Instances[i]
		in.Name = strings.TrimSpace(in.Name)
		if in.Name == "" {
			return nil, fmt.Errorf("instance %d: name is required", i+1)
		}
		in.ID = slug(in.Name)
		if in.ID == "" {
			return nil, fmt.Errorf("instance %q: name needs at least one letter or digit", in.Name)
		}
		if other, ok := seen[in.ID]; ok {
			return nil, fmt.Errorf("instances %q and %q have the same name", other, in.Name)
		}
		seen[in.ID] = in.Name
		in.Label = strings.TrimSpace(in.Label)
		if in.Label != "" && !in.ReadOnly {
			return nil, fmt.Errorf("instance %q: label needs readonly: true", in.Name)
		}
		if in.ReadOnly && in.Label == "" {
			in.Label = "readonly"
		}
		u, err := url.Parse(in.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("instance %q: url must look like http://host:4000", in.Name)
		}
	}
	return &c, nil
}

// slug lowercases s and joins its runs of letters and digits with dashes.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	return b.String()
}
