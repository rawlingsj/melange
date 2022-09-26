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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
	context := Context{}

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

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line != "" && strings.Contains(line, "=") {
			parts := strings.Split(line, "=")
			if len(parts) != 2 {
				return fmt.Errorf("too many parts found, expecting 2 found %v.  %s", len(parts), parts)
			}

			value, err := strconv.Unquote(parts[1])
			if err != nil {
				value = parts[1]
			}
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
	return nil
}
func (c Context) buildFetchStep() error {
	if c.ApkBuild.Source == "" {
		return fmt.Errorf("no source URL")
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

	if len(c.ApkBuild.SubPackages) > 0 {
		subpackage := build.Subpackage{
			Name: c.ApkBuild.PackageName + "-dev",
			Dependencies: build.Dependencies{
				Runtime: []string{c.ApkBuild.PackageName},
			},
			Pipeline:    []build.Pipeline{{Uses: "split/dev"}},
			Description: c.ApkBuild.PackageName + " headers",
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
