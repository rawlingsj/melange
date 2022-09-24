package convert

import (
	"bufio"
	"chainguard.dev/melange/pkg/build"
	"fmt"
	"github.com/pkg/errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type Context struct {
	ApkBuild       *ApkBuild
	ConfigFilename string
	OutDir         string
	Client         *http.Client
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
	build.Package
	generatedFromComment string `yaml:"#"`
}

func New(configFilename string) (Context, error) {
	context := Context{}

	err := validate(configFilename)
	if err != nil {
		return context, errors.Wrapf(err, "failed to validate config filename")
	}
	context.ConfigFilename = configFilename

	context.Client = &http.Client{}
	context.ApkBuild = &ApkBuild{}

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

func (c Context) getApkBuildFile() error {

	fullURLFile := c.ConfigFilename

	// Put content on file
	resp, err := c.Client.Get(fullURLFile)
	if err != nil {
		return errors.Wrapf(err, "getting %s", fullURLFile)
	}
	defer resp.Body.Close()

	err = c.parseApkBuild(resp.Body)
	if err != nil {
		return errors.Wrapf(err, "failed to parse apkbuild %s", fullURLFile)
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
func (c Context) getSourceSha() {

}

func (c Context) mapMelange() {

}

func (c Context) write() {

}

func (c Context) name() {

}
