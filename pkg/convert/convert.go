package convert

import (
	"bufio"
	apko_types "chainguard.dev/apko/pkg/build/types"
	"chainguard.dev/melange/pkg/build"
	"crypto/sha256"
	"fmt"
	"github.com/pkg/errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type context struct {
	ApkBuild               *ApkBuild
	*GeneratedMelageConfig `yaml:"-"`
	ConfigFilename         string
	OutDir                 string
	Client                 *http.Client
}
type ApkBuild struct {
	PackageName    string   `yaml:"pkgname"`
	PackageVersion string   `yaml:"pkgver"`
	PackageRel     string   `yaml:"pkgrel"`
	PackageDesc    string   `yaml:"pkgdesc"`
	PackageUrl     string   `yaml:"url"`
	Arch           []string `yaml:"arch"`
	License        string   `yaml:"license"`
	DependDev      []string `yaml:"depends_dev"`
	MakeDepends    []string `yaml:"makedepends"`
	SubPackages    []string `yaml:"subpackages"`
	Source         string   `yaml:"source"`
}
type GeneratedMelageConfig struct {
	Package              build.Package                 `yaml:"package"`
	Environment          apko_types.ImageConfiguration `yaml:"environment,omitempty"`
	Pipeline             []build.Pipeline              `yaml:"pipeline,omitempty"`
	Subpackages          []build.Subpackage            `yaml:"subpackages,omitempty"`
	generatedFromComment string                        `yaml:"#"`
}

func New(configFilename string) (context, error) {
	context := context{}

	err := validate(configFilename)
	if err != nil {
		return context, errors.Wrapf(err, "failed to validate config filename")
	}
	context.ConfigFilename = configFilename

	context.Client = &http.Client{}
	context.ApkBuild = &ApkBuild{}
	context.GeneratedMelageConfig = &GeneratedMelageConfig{}

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

func (c context) getApkBuildFile() error {

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

func (c context) parseApkBuild(r io.Reader) error {

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line != "" && strings.Contains(line, "=") {
			parts := strings.Split(line, "=")
			if len(parts) != 2 {
				return fmt.Errorf("too many parts found, expecting 2 found %v", len(parts))
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
func (c context) buildFetchStep() error {
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

	if !strings.HasSuffix(source, "tar.xz") && !strings.HasSuffix(source, "tar.gz") {
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
			"uri":             source,
			"expected-sha256": fmt.Sprintf("%x", expectedSha),
		},
	}
	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, pipeline)

	return nil
}

func (c context) mapMelange() {

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

	c.GeneratedMelageConfig.generatedFromComment = fmt.Sprintf("generated from file %s", c.ConfigFilename)
}

func (c context) write() {

}

func (c context) name() {

}
