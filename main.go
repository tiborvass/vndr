package main

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/LK4D4/vndr/build"
	"github.com/LK4D4/vndr/godl"
)

const (
	vendorDir  = "vendor"
	configFile = "vendor.conf"
)

var (
	verbose        bool
	useGomod       bool
	cleanWhitelist regexpSlice
	strict         bool
)

type regexpSlice []*regexp.Regexp

var _ flag.Value = new(regexpSlice)

func (rs *regexpSlice) Set(exp string) error {
	regex, err := regexp.Compile(exp)
	if err != nil {
		return err
	}

	*rs = append(*rs, regex)
	return nil
}

func (rs *regexpSlice) String() string {
	exps := []string{}
	for _, regex := range *rs {
		exps = append(exps, fmt.Sprintf("%q", regex.String()))
	}
	return fmt.Sprintf("%v", exps)
}

func (rs *regexpSlice) matchString(str string) bool {
	for _, regex := range *rs {
		if regex.MatchString(str) {
			return true
		}
	}
	return false
}

func init() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "%s [[import path] [revision]] [repository]\n%s init\n", os.Args[0], os.Args[0])
		flag.PrintDefaults()
	}
	e := os.Getenv("VNDR_GOMOD")
	flag.BoolVar(&verbose, "verbose", false, "shows all warnings")
	flag.BoolVar(&useGomod, "gomod", e != "" && e != "0", "use go mod for vendoring (relies on replace directive)")
	flag.Var(&cleanWhitelist, "whitelist", "regular expressions to whitelist for cleaning phase of vendoring, relative to the vendor/ directory")
	flag.BoolVar(&strict, "strict", false, "checking mode. treat non-trivial warning as an error")
}

func validateArgs() {
	if len(flag.Args()) > 3 {
		flag.Usage()
		os.Exit(2)
	}
	if flag.Arg(0) == "init" && len(flag.Args()) > 1 {
		flag.Usage()
		os.Exit(2)
	}
}

func checkUnused(deps []depEntry, vd string) {
	for _, d := range deps {
		if _, err := os.Stat(filepath.Join(vd, d.ImportPath)); err != nil && os.IsNotExist(err) {
			Warnf("package %s is unused, consider removing it from vendor.conf", d.ImportPath)
		}
	}
}

func checkLicense(deps []depEntry, vd string) {
	for _, d := range deps {
		dir := filepath.Join(vd, d.ImportPath)
		files, err := ioutil.ReadDir(dir)
		if err != nil {
			// err can be nil for unused package
			continue
		}
		licenseFiles := 0
		for _, file := range files {
			p := filepath.Join(dir, file.Name())
			if isLicenseFile(p) {
				licenseFiles++
			}
		}
		if licenseFiles == 0 && verbose {
			log.Printf("WARNING(verbose): package %s may lack license information", d.ImportPath)
		}
	}
}

func mergeDeps(root string, deps []depEntry) depEntry {
	merged := depEntry{ImportPath: root}
	merged.Rev = deps[0].Rev
	for _, d := range deps {
		if d.RepoPath != "" {
			merged.RepoPath = d.RepoPath
			break
		}
	}
	return merged
}

var rootImportCache = map[string]string{}

func rootImport(pkg string) (string, error) {
	cachedRoot, ok := rootImportCache[pkg]
	if ok {
		return cachedRoot, nil
	}
	root, err := godl.RootImport(pkg)
	if err != nil {
		return "", err
	}
	rootImportCache[pkg] = root
	return root, nil
}

func validateDeps(deps []depEntry) error {
	roots := make(map[string][]depEntry)
	var rootsOrder []string
	for _, d := range deps {
		root, err := rootImport(d.ImportPath)
		if err != nil {
			return err
		}
		if _, ok := roots[root]; !ok {
			rootsOrder = append(rootsOrder, root)
		}
		roots[root] = append(roots[root], d)
	}
	var newDeps []depEntry
	var invalid bool
	for _, r := range rootsOrder {
		rootDeps := roots[r]
		if len(rootDeps) == 1 {
			d := rootDeps[0]
			if d.ImportPath != r {
				Warnf("package %s is not root import, should be %s", d.ImportPath, r)
				invalid = true
				newDeps = append(newDeps, depEntry{ImportPath: r, Rev: d.Rev, RepoPath: d.RepoPath})
				continue
			}
			newDeps = append(newDeps, d)
			continue
		}
		invalid = true
		var imps []string
		for _, d := range rootDeps {
			imps = append(imps, d.ImportPath)
		}
		Warnf("packages '%s' has same root import %s", strings.Join(imps, ", "), r)
		newDeps = append(newDeps, mergeDeps(r, rootDeps))
	}
	if !invalid {
		return nil
	}
	tmpConfig := configFile + ".tmp"
	if err := writeConfig(newDeps, tmpConfig); err != nil {
		return err
	}
	Warnf("suggested vendor.conf is written to %s, use diff and common sense before using it", tmpConfig)
	return errors.New("There were some validation errors")
}

func getDeps(useGomod bool) ([]depEntry, error) {
	cfg, err := os.Open(configFile)
	if err != nil {
		return nil, fmt.Errorf("Failed to open config file: %v", err)
	}
	deps, err := parseDeps(cfg, useGomod)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse config: %v", err)
	}
	if err := validateDeps(deps); err != nil {
		return nil, err
	}
	return deps, nil
}

func getFlagDep(cfgDeps []depEntry) (depEntry, error) {
	dep := depEntry{
		ImportPath: flag.Arg(0),
		Rev:        flag.Arg(1),
		RepoPath:   flag.Arg(2),
	}
	// if there is no revision, try to find it in config
	if dep.Rev == "" {
		for _, d := range cfgDeps {
			if d.ImportPath == dep.ImportPath {
				dep.Rev = d.Rev
				dep.RepoPath = d.RepoPath
				break
			}
		}
		if dep.Rev == "" {
			return depEntry{}, fmt.Errorf("Failed to find %s in config file and revision was not specified", dep.ImportPath)
		}
	}
	return dep, nil
}

func main() {
	start := time.Now()
	defer func() {
		log.Printf("Running time: %v", time.Since(start))
	}()
	flag.Parse()
	validateArgs()

	if useGomod && exec.Command("go", "help", "mod").Run() != nil {
		log.Fatal("could not detect go mod support with 'go help mod'")
	}

	gp, err := getGOPATH()
	if err != nil {
		log.Fatal(err)
	}
	if gp == "" {
		log.Fatal("GOPATH is not set")
	}

	var init bool
	if flag.Arg(0) == "init" {
		if useGomod {
			log.Fatal("init unsupported with go mod")
		}
		init = true
		_, cerr := os.Stat(configFile)
		_, verr := os.Stat(vendorDir)
		if cerr == nil || verr == nil {
			log.Fatal("There must not be vendor dir and vendor.conf file for initialization")
		}
	}

	wd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Error getting working directory: %v", err)
	}
	wd, err = filepath.EvalSymlinks(wd)
	if err != nil {
		log.Fatalf("Error getting working directory after evalsymlinks: %v", err)
	}
	vd := filepath.Join(wd, vendorDir)

	if useGomod {
		if err := gomodVndr(wd, gp, verbose); err != nil {
			log.Fatal(err)
		}
		return
	}

	log.Println("Collecting initial packages")
	initPkgs, err := collectPkgs(wd)
	if err != nil {
		log.Fatalf("Error collecting initial packages: %v", err)
	}
	// variables for init
	var dlFunc func(string) (*build.Package, error)
	var deps []depEntry

	if !init {
		log.Println("Download dependencies")
		cfgDeps, err := getDeps(false /* useGomod */)
		if err != nil {
			log.Fatal(err)
		}

		if len(flag.Args()) != 0 {
			flagDep, err := getFlagDep(cfgDeps)
			if err != nil {
				log.Fatal(err)
			}
			cfgDeps = []depEntry{flagDep}
		} else {
			log.Println("Starting whole vndr cycle because no package specified")
			if err := os.RemoveAll(vd); err != nil {
				log.Fatal(err)
			}
		}
		startDownload := time.Now()
		if err := cloneAll(vd, cfgDeps); err != nil {
			log.Fatal(err)
		}

		deps = cfgDeps
		log.Printf("Dependencies downloaded. Download time: %v", time.Since(startDownload))
	} else {
		dlFunc = func(imp string) (*build.Package, error) {
			vcs, err := godl.Download(imp, "", filepath.Join(wd, vendorDir), "")
			if err != nil {
				return nil, err
			}
			rev, err := getRev(vcs)
			if err != nil {
				return nil, err
			}
			log.Printf("\tDownloaded %s, revision %s", imp, rev)
			deps = append(deps, depEntry{ImportPath: vcs.ImportPath, Rev: rev})

			pkg, err := ctx.Import(imp, wd, 0)
			if _, ok := err.(*build.MultiplePackageError); ok {
				return pkg, nil
			}
			return pkg, err
		}
		log.Println("Start vendoring initialization")
	}
	log.Println("Collecting all dependencies")
	pkgs, err := collectAllDeps(wd, dlFunc, initPkgs...)
	if err != nil {
		log.Fatalf("Error on collecting all dependencies: %v", err)
	}
	log.Println("Clean vendor dir from unused packages")
	for _, regex := range cleanWhitelist {
		log.Printf("\tIgnoring paths matching %q", regex.String())
	}
	if err := cleanVendor(vd, pkgs); err != nil {
		log.Fatal(err)
	}
	if init {
		if err := writeConfig(deps, configFile); err != nil {
			log.Fatal(err)
		}
		log.Println("Vendor initialized and result is in", configFile)
	} else {
		checkUnused(deps, vd)
	}
	checkLicense(deps, vd)
	if strict {
		if w := Warns(); len(w) > 0 {
			log.Fatalf("Treating %d warnings as errors", len(w))
		}
	}
	log.Println("Success")
}
