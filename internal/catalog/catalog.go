package catalog

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

var (
	ErrUnsupportedLanguage = errors.New("unsupported language code")
	ErrUnsupportedPair     = errors.New("unsupported language pair")
)

type Language struct {
	Code     string `json:"code" yaml:"code"`
	Name     string `json:"name" yaml:"name"`
	AsSource bool   `json:"as_source" yaml:"as_source"`
	AsTarget bool   `json:"as_target" yaml:"as_target"`
	Provider string `json:"provider" yaml:"provider"`
}

type Route struct {
	Src      string `json:"src" yaml:"src"`
	Dst      string `json:"dst" yaml:"dst"`
	Provider string `json:"provider" yaml:"provider"`
}

type ConfigFile struct {
	DefaultProvider string     `yaml:"default_provider"`
	Languages       []Language `yaml:"languages"`
	Routes          []Route    `yaml:"routes"`
}

type Catalog struct {
	mu              sync.RWMutex
	defaultProvider string
	languages       []Language
	langMap         map[string]Language
	routeMap        map[string]string // "src:dst" -> provider
}

func LoadFromFile(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading catalog file: %w", err)
	}

	var cfg ConfigFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing catalog yaml: %w", err)
	}

	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = "realtime"
	}

	cat := &Catalog{
		defaultProvider: cfg.DefaultProvider,
		languages:       cfg.Languages,
		langMap:         make(map[string]Language, len(cfg.Languages)),
		routeMap:        make(map[string]string, len(cfg.Routes)),
	}

	for _, l := range cfg.Languages {
		cat.langMap[l.Code] = l
	}

	for _, r := range cfg.Routes {
		cat.routeMap[r.Src+":"+r.Dst] = r.Provider
	}

	return cat, nil
}

func (c *Catalog) Languages() []Language {
	c.mu.RLock()
	defer c.mu.RUnlock()
	res := make([]Language, len(c.languages))
	copy(res, c.languages)
	return res
}

func (c *Catalog) ValidatePair(src, dst string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	s, okS := c.langMap[src]
	if !okS || !s.AsSource {
		return fmt.Errorf("%w: source %q", ErrUnsupportedLanguage, src)
	}

	d, okD := c.langMap[dst]
	if !okD || !d.AsTarget {
		return fmt.Errorf("%w: destination %q", ErrUnsupportedLanguage, dst)
	}

	if src == dst {
		return fmt.Errorf("%w: source and destination languages cannot be the same", ErrUnsupportedPair)
	}

	return nil
}

func (c *Catalog) ResolveProvider(src, dst string) (string, error) {
	if err := c.ValidatePair(src, dst); err != nil {
		return "", err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	// 1. Explicit pair route
	if p, ok := c.routeMap[src+":"+dst]; ok && p != "" {
		return p, nil
	}

	// 2. Destination language provider
	if d, ok := c.langMap[dst]; ok && d.Provider != "" {
		return d.Provider, nil
	}

	// 3. Fallback default
	return c.defaultProvider, nil
}
