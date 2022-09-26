package convert

import (
	"bufio"
	apkotypes "chainguard.dev/apko/pkg/build/types"
	"chainguard.dev/melange/pkg/build"
	"crypto/sha256"
	"fmt"
	"github.com/pkg/errors"
	"golang.org/x/exp/slices"
	"gopkg.in/yaml.v3"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Context struct {
	ApkBuild               *ApkBuild
	*GeneratedMelageConfig `yaml:"-"`
	ConfigFilename         string
	OutDir                 string
	AdditionalRepositories []string
	AdditionalKeyrings     []string
	Client                 *http.Client
	Logger                 *log.Logger
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

func New(configFilename, outDir string) (Context, error) {
	context := Context{
		Logger: log.New(log.Writer(), "melange: ", log.LstdFlags|log.Lmsgprefix),
	}

	err := validate(configFilename)
	if err != nil {
		return context, errors.Wrapf(err, "failed to validate config filename")
	}
	context.ConfigFilename = configFilename

	context.Client = &http.Client{}
	context.ApkBuild = &ApkBuild{}
	context.GeneratedMelageConfig = &GeneratedMelageConfig{}

	context.OutDir = outDir

	return context, nil
}

func validate(configFile string) error {
	//todo validate file

	// Build fileName from fullPath
	//fileURL, err := url.Parse(fullURLFile)
	//if err != nil {
	//	log.Fatal(err)
	//}
	return nil
}

func (c Context) Generate() error {

	// get the contents of the APKBUILD file
	err := c.getApkBuildFile()
	if err != nil {
		return errors.Wrap(err, "getting apk build file")
	}

	// generate any dependencies first
	err = c.generateDependencies()
	if err != nil {
		return errors.Wrap(err, "getting apk build file")
	}

	// automatically add a fetch step to the melange config to fetch the source
	err = c.buildFetchStep()
	if err != nil {
		return errors.Wrap(err, "building fetch step")
	}

	// maps the APKBUILD values to melange config
	c.mapMelange()

	// builds the melange environment configuration
	c.buildEnvironment()

	err = c.write()
	if err != nil {
		return errors.Wrap(err, "writing melange config file")
	}

	return nil
}

func (c Context) getApkBuildFile() error {

	resp, err := c.Client.Get(c.ConfigFilename)
	if err != nil {
		return errors.Wrapf(err, "getting %s", c.ConfigFilename)
	}
	defer resp.Body.Close()

	err = c.parseApkBuild(resp.Body)
	if err != nil {
		return errors.Wrapf(err, "failed to parse apkbuild %s", c.ConfigFilename)
	}
	return nil
}

func (c Context) parseApkBuild(r io.Reader) error {

	// turn into byte array else scanner skips lines
	b, err := io.ReadAll(r)
	if err != nil {
		return errors.Wrapf(err, "reading APKBUILD file")
	}

	scanner := bufio.NewScanner(strings.NewReader(string(b)))
	scanned := false
	for scanner.Scan() {
		scanned = true
		line := strings.TrimSpace(scanner.Text())
		if line != "" && strings.Contains(line, "=") {
			parts := strings.Split(line, "=")

			value := strings.ReplaceAll(parts[1], "\"", "")
			value = strings.TrimSpace(value)

			switch parts[0] {

			case "pkgname":
				c.ApkBuild.PackageName = value
			case "pkgver":
				c.ApkBuild.PackageVersion = value
			case "pkgrel":
				c.ApkBuild.PackageRel = value
			case "pkgdesc":
				c.ApkBuild.PackageDesc = value
			case "url":
				c.ApkBuild.PackageUrl = value
			case "arch":
				c.ApkBuild.Arch = strings.Split(value, " ")
			case "license":
				c.ApkBuild.License = value
			case "depends_dev":
				c.ApkBuild.DependDev = strings.Split(value, " ")
			case "subpackages":
				c.ApkBuild.SubPackages = strings.Split(value, " ")
			case "makedepends":
				c.ApkBuild.MakeDepends = strings.Split(value, " ")
			case "source":
				c.ApkBuild.Source = value
			}
		}
	}
	if !scanned {
		return fmt.Errorf("not scanned file %s", c.ConfigFilename)
	}
	return nil
}
func (c Context) buildFetchStep() error {
	if c.ApkBuild.Source == "" {
		return fmt.Errorf("no source URL for APKBUILD %s pknname %s", c.ConfigFilename, c.ApkBuild.PackageName)
	}
	if c.ApkBuild.PackageVersion == "" {
		return fmt.Errorf("no package version")
	}
	source := strings.ReplaceAll(c.ApkBuild.Source, "$pkgver", c.ApkBuild.PackageVersion)
	_, err := url.ParseRequestURI(source)
	if err != nil {
		return errors.Wrapf(err, "parsing URI %s", source)
	}

	if !strings.HasSuffix(source, "tar.xz") && !strings.HasSuffix(source, "tar.gz") && !strings.HasSuffix(source, "bz2") {
		return fmt.Errorf("only tar.xz and tar.gz currently supported")
	}

	resp, err := c.Client.Get(source)
	if err != nil {
		return errors.Wrapf(err, "failed getting URI %s", c.ConfigFilename)
	}
	defer resp.Body.Close()

	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return errors.Wrapf(err, "generating sha265 for %s", c.ConfigFilename)
	}

	expectedSha := h.Sum(nil)

	pipeline := build.Pipeline{
		Uses: "fetch",
		With: map[string]string{
			"uri":             strings.ReplaceAll(source, c.ApkBuild.PackageVersion, "${{package.version}}"),
			"expected-sha256": fmt.Sprintf("%x", expectedSha),
		},
	}
	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, pipeline)

	return nil
}

func (c Context) mapMelange() {

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

func (c Context) write() error {

	actual, err := yaml.Marshal(&c.GeneratedMelageConfig)
	if err != nil {
		return errors.Wrapf(err, "marshalling melange configuration")
	}

	if _, err := os.Stat(c.OutDir); os.IsNotExist(err) {
		err = os.MkdirAll(c.OutDir, os.ModePerm)
		if err != nil {
			return errors.Wrapf(err, "creating output directory %s", c.OutDir)
		}

	}

	melangeFile := filepath.Join(c.OutDir, c.ApkBuild.PackageName+".yaml")
	f, err := os.Create(melangeFile)
	if err != nil {
		return errors.Wrapf(err, "creating file %s", melangeFile)
	}
	defer f.Close()

	_, err = f.WriteString(string(actual))
	return err
}

func (c Context) buildEnvironment() {

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
	c.Logger.Printf("addition %s", c.AdditionalRepositories)
	env.Contents.Repositories = append(env.Contents.Repositories, c.AdditionalRepositories...)
	env.Contents.Keyring = append(env.Contents.Keyring, c.AdditionalKeyrings...)

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

func (c Context) generateDependencies() error {
	for _, d := range c.ApkBuild.MakeDepends {
		c.foo(d)
	}
	for _, d := range c.ApkBuild.DependDev {
		c.foo(d)
	}
	return nil
}

func (c Context) foo(d string) {

	if d != "$depends_dev" {
		d = strings.TrimSuffix(d, "-dev")
		dependencyApkBuild := strings.ReplaceAll(c.ConfigFilename, c.ApkBuild.PackageName, d)

		gc, err := New(dependencyApkBuild, c.OutDir)
		if err != nil {
			c.Logger.Printf("failed: " + err.Error())
		}
		gc.AdditionalRepositories = append(gc.AdditionalRepositories, c.AdditionalRepositories...)
		gc.AdditionalKeyrings = append(gc.AdditionalKeyrings, c.AdditionalKeyrings...)
		err = gc.Generate()
		if err != nil {
			c.Logger.Printf("failed to generate: " + err.Error())
		}
	}
}
