package main

import (
	"html/template"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

func gomodVndr(wd, gp string, verbose bool) error {
	modulepath, err := filepath.Rel(filepath.Join(gp, "src"), wd)
	if err != nil {
		return err
	}
	cfgDeps, err := getDeps(true /* useGomod */)
	if err != nil {
		return err
	}
	gomodPath := filepath.Join(wd, "go.mod")
	f, err := os.Create(gomodPath)
	if err != nil {
		return err
	}
	if err := writeGomod(f, modulepath, cfgDeps); err != nil {
		return err
	}
	vendor := []string{"mod", "vendor"}
	if verbose {
		vendor = append(vendor, "-v")
	}
	cmd := exec.Command("go", vendor...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	if verbose {
		f, err := os.Open(gomodPath)
		if err != nil {
			return err
		}
		io.Copy(os.Stdout, f)
		f.Close()
	}
	os.Remove(gomodPath)
	os.Remove(filepath.Join(wd, "go.sum"))
	return nil
}

func writeGomod(f *os.File, modulepath string, ds []depEntry) error {
	defer f.Close()
	return gomodTpl.Execute(f, struct {
		ModulePath string
		Deps       []depEntry
	}{
		modulepath,
		ds,
	})
}

var gomodTpl = template.Must(template.New("gomod").Parse(`module {{.ModulePath}}
{{if .Deps}}
replace (
	{{- range .Deps}}
	{{.ImportPath}} => {{if .RepoPath}}{{.RepoPath}}{{else}}{{.ImportPath}}{{end}} {{.Rev}}
	{{- end}}
)
{{end}}
`))
