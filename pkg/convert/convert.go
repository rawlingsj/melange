package convert

import (
	"bufio"
	apkotypes "chainguard.dev/apko/pkg/build/types"
	"chainguard.dev/melange/pkg/build"
	"crypto/sha256"
	"fmt"
	"github.com/pkg/errors"
	"golang.org/x/exp/slices"
	"golang.org/x/time/rate"
	"gopkg.in/yaml.v3"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Context struct {
	ApkConvertors          map[string]ApkConvertor
	OutDir                 string
	AdditionalRepositories []string
	AdditionalKeyrings     []string
	Client                 *RLHTTPClient
	Logger                 *log.Logger
}
type ApkConvertor struct {
	*ApkBuild
	ApkBuildRaw            []byte
	*GeneratedMelageConfig `yaml:"-"`
	ConfigFilename         string
}
type ApkBuild struct {
	PackageName    string
	PackageVersion string
	PackageRel     string
	PackageDesc    string
	PackageUrl     string
	Arch           []string
	License        string
	DependDev      []string
	MakeDepends    []string
	SubPackages    []string
	Source         string
}
type GeneratedMelageConfig struct {
	Package     build.Package                `yaml:"package"`
	Environment apkotypes.ImageConfiguration `yaml:"environment,omitempty"`
	Pipeline    []build.Pipeline             `yaml:"pipeline,omitempty"`
	Subpackages []build.Subpackage           `yaml:"subpackages,omitempty"`
	//GeneratedFromComment string                        `yaml:"#"` //todo figure out how to add unescaped comments
}

func New() (Context, error) {
	context := Context{
		ApkConvertors: map[string]ApkConvertor{},
		Client: &RLHTTPClient{
			client:      http.DefaultClient,
			Ratelimiter: rate.NewLimiter(rate.Every(1*time.Second), 1), // 10 request every 10 seconds
		},
		Logger: log.New(log.Writer(), "melange: ", log.LstdFlags|log.Lmsgprefix),
	}

	return context, nil
}

func (c Context) Generate(apkBuildURI string) error {

	// get the contents of the APKBUILD file
	err := c.getApkBuildFile(apkBuildURI)
	if err != nil {
		return errors.Wrap(err, "getting apk build file")
	}

	// generate any dependencies first
	//err = c.generateDependencies()
	if err != nil {
		return errors.Wrap(err, "getting apk build file")
	}

	// build map of dependencies
	err = c.buildMapOfDependencies(apkBuildURI)
	if err != nil {
		return errors.Wrap(err, "building map of dependencies")
	}

	// todo reverse map order so we generate lowest transitive dependency first
	// this will help to build melange config in the right order

	// loop over map and generate melange config for each
	for _, apkConverter := range c.ApkConvertors {

		// automatically add a fetch step to the melange config to fetch the source
		err = c.buildFetchStep(apkConverter)
		if err != nil {
			return errors.Wrap(err, "building fetch step")
		}

		// maps the APKBUILD values to melange config
		apkConverter.mapMelange()

		// builds the melange environment configuration
		apkConverter.buildEnvironment()

		err = apkConverter.write(c.OutDir)
		if err != nil {
			return errors.Wrap(err, "writing melange config file")
		}
	}

	return nil
}

func (c Context) getApkBuildFile(apkFilename string) error {

	req, _ := http.NewRequest("GET", apkFilename, nil)
	resp, err := c.Client.Do(req)

	if err != nil {
		return errors.Wrapf(err, "getting %s", apkFilename)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("non ok http response code: %v", resp.StatusCode)
	}

	err = c.parseApkBuild(resp.Body, apkFilename)
	if err != nil {
		return errors.Wrapf(err, "failed to parse apkbuild %s", apkFilename)
	}
	return nil
}

func (c Context) parseApkBuild(r io.Reader, key string) error {

	c.ApkConvertors[key] = ApkConvertor{
		ApkBuild:              &ApkBuild{},
		GeneratedMelageConfig: &GeneratedMelageConfig{},
	}

	apkbuild := c.ApkConvertors[key].ApkBuild

	// turn into byte array else scanner skips lines
	b, err := io.ReadAll(r)
	if err != nil {
		return errors.Wrapf(err, "reading APKBUILD file")
	}

	scanner := bufio.NewScanner(strings.NewReader(string(b)))

	for scanner.Scan() {

		line := strings.TrimSpace(scanner.Text())
		if line != "" && strings.Contains(line, "=") {
			parts := strings.Split(line, "=")

			value := strings.ReplaceAll(parts[1], "\"", "")
			value = strings.TrimSpace(value)

			switch parts[0] {

			case "pkgname":
				apkbuild.PackageName = value
			case "pkgver":
				apkbuild.PackageVersion = value
			case "pkgrel":
				apkbuild.PackageRel = value
			case "pkgdesc":
				apkbuild.PackageDesc = value
			case "url":
				apkbuild.PackageUrl = value
			case "arch":
				apkbuild.Arch = strings.Split(value, " ")
			case "license":
				apkbuild.License = value
			case "depends_dev":
				apkbuild.DependDev = strings.Split(value, " ")
			case "subpackages":
				apkbuild.SubPackages = strings.Split(value, " ")
			case "makedepends":
				apkbuild.MakeDepends = strings.Split(value, " ")
			case "source":
				apkbuild.Source = value
			}
		}
	}

	return nil
}

func (c Context) buildFetchStep(converter ApkConvertor) error {

	apkBuild := converter.ApkBuild

	if apkBuild.Source == "" {
		c.Logger.Printf("skip adding pipeline for package %s, no source URL found", converter.PackageName)
		return nil
	}
	if apkBuild.PackageVersion == "" {
		return fmt.Errorf("no package version")
	}
	source := strings.ReplaceAll(apkBuild.Source, "$pkgver", apkBuild.PackageVersion)
	_, err := url.ParseRequestURI(source)
	if err != nil {
		return errors.Wrapf(err, "parsing URI %s", source)
	}

	if !strings.HasSuffix(source, "tar.xz") && !strings.HasSuffix(source, "tar.gz") && !strings.HasSuffix(source, "bz2") && !strings.HasSuffix(source, "zip") {
		return fmt.Errorf("only tar.xz and tar.gz currently supported")
	}

	req, _ := http.NewRequest("GET", source, nil)
	resp, err := c.Client.Do(req)

	if err != nil {
		return errors.Wrapf(err, "failed getting URI %s", source)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("non ok http response code: %v", resp.StatusCode)
	}

	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return errors.Wrapf(err, "generating sha265 for %s", source)
	}

	expectedSha := h.Sum(nil)

	pipeline := build.Pipeline{
		Uses: "fetch",
		With: map[string]string{
			"uri":             strings.ReplaceAll(source, apkBuild.PackageVersion, "${{package.version}}"),
			"expected-sha256": fmt.Sprintf("%x", expectedSha),
		},
	}
	converter.GeneratedMelageConfig.Pipeline = append(converter.GeneratedMelageConfig.Pipeline, pipeline)

	return nil
}

func (c ApkConvertor) mapMelange() {

	c.GeneratedMelageConfig.Package.Name = c.ApkBuild.PackageName
	c.GeneratedMelageConfig.Package.Description = c.ApkBuild.PackageDesc
	c.GeneratedMelageConfig.Package.Version = c.ApkBuild.PackageVersion
	c.GeneratedMelageConfig.Package.TargetArchitecture = c.ApkBuild.Arch

	copyright := build.Copyright{
		Paths:       []string{"*"},
		Attestation: "TODO",
		License:     c.ApkBuild.License,
	}
	c.GeneratedMelageConfig.Package.Copyright = append(c.GeneratedMelageConfig.Package.Copyright, copyright)

	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "autoconf/configure"})
	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "autoconf/make"})
	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "autoconf/make-install"})

	for _, subPackage := range c.ApkBuild.SubPackages {
		subpackage := build.Subpackage{
			Name: strings.Replace(subPackage, "$pkgname", c.ApkBuild.PackageName, 1),
		}

		// generate subpackages based on the subpackages defined in the APKBUILD
		var ext string
		parts := strings.Split(subPackage, "-")
		if len(parts) == 2 {
			switch parts[1] {
			case "doc":
				ext = "manpages"
			case "dev":
				ext = "dev"
				subpackage.Dependencies = build.Dependencies{
					Runtime: []string{c.ApkBuild.PackageName},
				}
			case "locales":
				ext = "dev"
			default:
				// if we don't recognise the extension make it obvious user needs to manually fix the config
				ext = "FIXME"
			}

			subpackage.Pipeline = []build.Pipeline{{Uses: "split/" + ext}}
			subpackage.Description = c.ApkBuild.PackageName + " " + ext

		} else {
			// if we don't recognise the extension make it obvious user needs to manually fix the config
			subpackage.Pipeline = []build.Pipeline{{Runs: "FIXME"}}
		}

		c.GeneratedMelageConfig.Subpackages = append(c.GeneratedMelageConfig.Subpackages, subpackage)
	}

	// todo add back once unescaped comments can be marshalled
	//c.GeneratedMelageConfig.GeneratedFromComment = fmt.Sprintf("generated from file %s", c.ConfigFilename)
}

func (c ApkConvertor) write(outdir string) error {

	actual, err := yaml.Marshal(&c.GeneratedMelageConfig)
	if err != nil {
		return errors.Wrapf(err, "marshalling melange configuration")
	}

	if _, err := os.Stat(outdir); os.IsNotExist(err) {
		err = os.MkdirAll(outdir, os.ModePerm)
		if err != nil {
			return errors.Wrapf(err, "creating output directory %s", outdir)
		}
	}

	// write the melange config
	melangeFile := filepath.Join(outdir, c.ApkBuild.PackageName+".yaml")
	f, err := os.Create(melangeFile)
	if err != nil {
		return errors.Wrapf(err, "creating file %s", melangeFile)
	}
	defer f.Close()

	_, err = f.WriteString(string(actual))

	return err
}

func (c ApkConvertor) buildEnvironment() {

	env := apkotypes.ImageConfiguration{
		Contents: struct {
			Repositories []string `yaml:"repositories"`
			Keyring      []string `yaml:"keyring"`
			Packages     []string `yaml:"packages"`
		}{
			Repositories: []string{
				"https://packages.wolfi.dev/bootstrap/stage3",
				"https://packages.wolfi.dev/os",
			},
			Keyring: []string{
				"https://packages.wolfi.dev/bootstrap/stage3/wolfi-signing.rsa.pub",
				"https://packages.wolfi.dev/os/wolfi-signing.rsa.pub",
			},
			Packages: []string{
				"busybox",
				"ca-certificates-bundle",
				"build-base",
				"automake",
				"autoconf",
			},
		},
	}
	//todo add back in
	//env.Contents.Repositories = append(env.Contents.Repositories, c.AdditionalRepositories...)
	//env.Contents.Keyring = append(env.Contents.Keyring, c.AdditionalKeyrings...)
	env.Contents.Packages = append(env.Contents.Packages, c.ApkBuild.MakeDepends...)

	for _, d := range c.ApkBuild.DependDev {
		if !strings.HasSuffix(d, "-dev") {
			d = d + "-dev"
		}
		env.Contents.Packages = append(env.Contents.Packages, d)
	}

	for i, p := range env.Contents.Packages {
		if p == "$depends_dev" {
			env.Contents.Packages = slices.Delete(env.Contents.Packages, i, i+1)
			break
		}
	}
	c.Environment = env
}

//
//func (c Context) generateDependencies() error {
//	for _, d := range c.ApkBuild.MakeDepends {
//		c.foo(d)
//	}
//	for _, d := range c.ApkBuild.DependDev {
//		c.foo(d)
//	}
//	return nil
//}
//
//func (c Context) foo(d string) {
//
//	if d != "$depends_dev" {
//		d = strings.TrimSuffix(d, "-dev")
//		dependencyApkBuild := strings.ReplaceAll(c.ConfigFilename, c.ApkBuild.PackageName, d)
//
//		gc, err := New(dependencyApkBuild, c.OutDir)
//		if err != nil {
//			c.Logger.Printf("failed: " + err.Error())
//		}
//		gc.AdditionalRepositories = append(gc.AdditionalRepositories, c.AdditionalRepositories...)
//		gc.AdditionalKeyrings = append(gc.AdditionalKeyrings, c.AdditionalKeyrings...)
//		gc.Client = c.Client
//
//		err = gc.Generate()
//		if err != nil {
//			c.Logger.Printf("failed to generate: " + err.Error())
//		}
//	}
//}

// gather deps, add to map, loop deps, fetch their deps, add to map
func (c Context) buildMapOfDependencies(apkBuildURI string) error {

	convertor, exists := c.ApkConvertors[apkBuildURI]
	if !exists {
		return fmt.Errorf("no top level apk convertor found for URI %s", apkBuildURI)
	}

	var deps []string

	// if make dependencies includes a reference to dev_depends var then add them to the deps list
	for _, dep := range convertor.ApkBuild.MakeDepends {
		if dep == "$depends_dev" {
			deps = append(deps, convertor.ApkBuild.DependDev...)
		} else {
			deps = append(deps, dep)
		}
	}

	// recursively loop round and add any missing dependencies to the map
	for _, dep := range deps {
		// using the same base URI switch the existing package name for the dependency and get related APKBUILD
		dependencyApkBuildURI := strings.ReplaceAll(apkBuildURI, convertor.ApkBuild.PackageName, dep)

		// if we don't already have this dependency in the map, go get it
		_, exists = c.ApkConvertors[dependencyApkBuildURI]
		if !exists {
			err := c.getApkBuildFile(dependencyApkBuildURI)
			if err != nil {
				// log and skip this dependency if there's an issue getting the APKBUILD as we are guessing the location of the APKBUILD
				c.Logger.Printf("failed to get APKBUILD %s", dependencyApkBuildURI)
				continue
			}

			err = c.buildMapOfDependencies(dependencyApkBuildURI)
			if err != nil {
				return errors.Wrap(err, "building map of dependencies")
			}
		}
	}
	return nil
}

//todo get a map of existing wolfi packages and don't generate a melange config if exists
