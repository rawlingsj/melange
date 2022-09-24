package convert

import (
	"bufio"
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
	ApkBuild              *ApkBuild
	GeneratedMelageConfig *GeneratedMelageConfig
	ConfigFilename        string
	OutDir                string
	Client                *http.Client
}
type ApkBuild struct {
	PackageName    string   `yaml:"pkgname"`
	PackageVersion string   `yaml:"pkgver"`
	PackageRel     string   `yaml:"pkgrel"`
	PackageDesc    string   `yaml:"pkgdesc"`
	PackageUrl     string   `yaml:"url"`
	Arch           string   `yaml:"arch"`
	License        string   `yaml:"license"`
	DependDev      []string `yaml:"depends_dev"`
	MakeDepends    []string `yaml:"makedepends"`
	SubPackages    []string `yaml:"subpackages"`
	Source         string   `yaml:"source"`
}
type GeneratedMelageConfig struct {
	build.Configuration
	generatedFromComment string `yaml:"#"`
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
				c.ApkBuild.Arch = value
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
	source := strings.ReplaceAll(c.ApkBuild.Source, "$pkgver", c.ApkBuild.PackageVersion)
	_, err := url.ParseRequestURI(source)
	if err != nil {
		return errors.Wrapf(err, "parsing URI %s", source)
	}

	if !strings.HasSuffix(source, "tar.xz") || !strings.HasSuffix(source, "tar.xz") {
		return fmt.Errorf("only tar.xz and tar.xz currently supported")
	}

	resp, err := c.Client.Get(source)
	if err != nil {
		return errors.Wrapf(err, "getting %s", c.ConfigFilename)
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

}

func (c context) write() {

}

func (c context) name() {

}
