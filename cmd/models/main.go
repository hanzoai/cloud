package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/hanzoai/ai/controllers"
	"gopkg.in/yaml.v3"
)

// env is the key ai's bootstrap reads the config path from.
const env = "modelConfigPath"

type values struct {
	Env []struct {
		Name  string `yaml:"name"`
		Value string `yaml:"value"`
	} `yaml:"env"`
	VolumeMounts []struct {
		Name      string `yaml:"name"`
		MountPath string `yaml:"mountPath"`
		SubPath   string `yaml:"subPath"`
	} `yaml:"volumeMounts"`
	Volumes []struct {
		Name      string `yaml:"name"`
		ConfigMap *struct {
			Name  string `yaml:"name"`
			Items []struct {
				Key  string `yaml:"key"`
				Path string `yaml:"path"`
			} `yaml:"items"`
		} `yaml:"configMap"`
	} `yaml:"volumes"`
	ConfigMaps []struct {
		Name  string            `yaml:"name"`
		Data  map[string]string `yaml:"data"`
		Files map[string]string `yaml:"files"`
	} `yaml:"configMaps"`
}

// config returns the model config text the values file mounts, and where it
// came from, or an error naming the first link of the wiring that is missing.
// chart is the chart directory a files: entry is relative to.
func config(raw []byte, chart string) (text, from string, err error) {
	var v values
	if err := yaml.Unmarshal(raw, &v); err != nil {
		return "", "", fmt.Errorf("values: %w", err)
	}
	want := ""
	for _, e := range v.Env {
		if e.Name == env {
			want = e.Value
		}
	}
	if want == "" {
		return "", "", fmt.Errorf("no %s in env", env)
	}
	vol, rel := "", ""
	for _, m := range v.VolumeMounts {
		switch {
		case m.MountPath == want && m.SubPath != "":
			vol, rel = m.Name, m.SubPath
		case strings.HasPrefix(want, strings.TrimSuffix(m.MountPath, "/")+"/"):
			vol, rel = m.Name, path.Join(m.SubPath, strings.TrimPrefix(want, strings.TrimSuffix(m.MountPath, "/")+"/"))
		}
	}
	if vol == "" {
		return "", "", fmt.Errorf("no volumeMount serves %s=%s", env, want)
	}
	cm, key := "", ""
	for _, vv := range v.Volumes {
		if vv.Name != vol {
			continue
		}
		if vv.ConfigMap == nil {
			return "", "", fmt.Errorf("volume %q is not a configMap", vol)
		}
		cm, key = vv.ConfigMap.Name, rel
		if len(vv.ConfigMap.Items) > 0 {
			key = ""
			for _, it := range vv.ConfigMap.Items {
				if it.Path == rel {
					key = it.Key
				}
			}
		}
	}
	if cm == "" {
		return "", "", fmt.Errorf("no volume %q", vol)
	}
	if key == "" {
		return "", "", fmt.Errorf("configMap volume %q projects no item at %q", vol, rel)
	}
	from = cm + "/" + key
	for _, c := range v.ConfigMaps {
		if c.Name != cm {
			continue
		}
		if t, ok := c.Data[key]; ok {
			return t, from, nil
		}
		if f, ok := c.Files[key]; ok {
			b, err := os.ReadFile(filepath.Join(chart, f))
			return string(b), from, err
		}
		return "", "", fmt.Errorf("configMap %q has no key %q", cm, key)
	}
	return "", "", fmt.Errorf("configMap %q is not declared in this file", cm)
}

// check loads the config a values file mounts with the pinned loader.
func check(file string) (string, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	// charts/app/values/<namespace>/<service>.yaml → charts/app
	chart := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	text, from, err := config(raw, chart)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "models-*.yaml")
	if err != nil {
		return from, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(text); err != nil {
		return from, err
	}
	if err := tmp.Close(); err != nil {
		return from, err
	}
	if err := controllers.InitModelConfig(tmp.Name()); err != nil {
		return from, fmt.Errorf("%s does not load: %w", from, err)
	}
	return from, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: models <values.yaml>...")
		os.Exit(2)
	}
	bad := 0
	for _, f := range os.Args[1:] {
		from, err := check(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
			bad++
			continue
		}
		fmt.Printf("%s: %s loads\n", f, from)
	}
	if bad > 0 {
		os.Exit(1)
	}
}
