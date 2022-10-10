package convert

import (
	apkotypes "chainguard.dev/apko/pkg/build/types"
	"chainguard.dev/melange/pkg/build"
	"chainguard.dev/melange/pkg/convert/wolfios"
	"crypto/sha256"
	"fmt"
	"github.com/pkg/errors"
	"gitlab.alpinelinux.org/alpine/go/apkbuild"
	"golang.org/x/exp/slices"
	"golang.org/x/time/rate"
	"gopkg.in/yaml.v3"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Context struct {
	*NavigationMap
	OutDir                 string
	AdditionalRepositories []string
	AdditionalKeyrings     []string
	Client                 *RLHTTPClient
	Logger                 *log.Logger
	WolfiOSPackages        map[string][]string
}
type NavigationMap struct {
	ApkConvertors map[string]ApkConvertor
	OrderedKeys   []string
}

type Dependency struct {
	Name string
}
type ApkConvertor struct {
	*apkbuild.Apkbuild
	ApkBuildRaw            []byte
	*GeneratedMelageConfig `yaml:"-"`
}

type GeneratedMelageConfig struct {
	Package              build.Package                `yaml:"package"`
	Environment          apkotypes.ImageConfiguration `yaml:"environment,omitempty"`
	Pipeline             []build.Pipeline             `yaml:"pipeline,omitempty"`
	Subpackages          []build.Subpackage           `yaml:"subpackages,omitempty"`
	GeneratedFromComment string                       `yaml:"-"`
}

// New initialise including a map of existing wolfios packages
func New() (Context, error) {
	context := Context{
		NavigationMap: &NavigationMap{
			ApkConvertors: map[string]ApkConvertor{},
			OrderedKeys:   []string{},
		},

		Client: &RLHTTPClient{
			client: http.DefaultClient,

			// 1 request every second to avoid DOS'ing server
			Ratelimiter: rate.NewLimiter(rate.Every(1*time.Second), 1),
		},
		Logger: log.New(log.Writer(), "melange: ", log.LstdFlags|log.Lmsgprefix),
	}

	req, _ := http.NewRequest("GET", wolfios.WolfiosPackageRepository, nil)
	resp, err := context.Client.Do(req)

	if err != nil {
		return context, errors.Wrapf(err, "failed getting URI %s", wolfios.WolfiosPackageRepository)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return context, fmt.Errorf("non ok http response for URI %s code: %v", wolfios.WolfiosPackageRepository, resp.StatusCode)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return context, errors.Wrap(err, "reading APKBUILD file")
	}

	// keep the map of wolfi packages on the main struct so it's easy to check if we already have any ABKBUILD dependencies
	context.WolfiOSPackages, err = wolfios.ParseWolfiPackages(b)
	if err != nil {
		return context, errors.Wrapf(err, "parsing wolfi packages")
	}

	return context, nil
}

func ReverseSlice[T comparable](s []T) {
	sort.SliceStable(s, func(i, j int) bool {
		return i > j
	})
}

func (c Context) Generate(apkBuildURI, pkgName string) error {

	// get the contents of the APKBUILD file
	err := c.getApkBuildFile(apkBuildURI, pkgName)
	if err != nil {
		return errors.Wrap(err, "getting apk build file")
	}

	// build map of dependencies
	err = c.buildMapOfDependencies(apkBuildURI, pkgName)
	if err != nil {
		return errors.Wrap(err, "building map of dependencies")
	}

	// reverse map order, so we generate the lowest transitive dependency first
	// this will help to build melange configs in the correct order
	ReverseSlice(c.OrderedKeys)

	// loop over map and generate melange config for each
	for i, key := range c.OrderedKeys {

		apkConverter := c.ApkConvertors[key]

		// automatically add a fetch step to the melange config to fetch the source
		err = c.buildFetchStep(apkConverter)
		if err != nil {
			// lets not error if we can't automatically add the fetch step
			c.Logger.Printf("skipping fetch step for %s", err.Error())
		}

		// maps the APKBUILD values to melange config
		apkConverter.mapMelange()

		// builds the melange environment configuration
		apkConverter.buildEnvironment(c.AdditionalRepositories, c.AdditionalKeyrings)

		err = apkConverter.write(strconv.Itoa(i), c.OutDir)
		if err != nil {
			return errors.Wrap(err, "writing melange config file")
		}
	}

	return nil
}
func (c Context) getApkBuildFile(apkbuildURL, packageName string) error {

	req, _ := http.NewRequest("GET", apkbuildURL, nil)
	resp, err := c.Client.Do(req)

	if err != nil {
		return errors.Wrapf(err, "getting %s", apkbuildURL)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("non ok http response code: %v", resp.StatusCode)
	}
	apkbuildFile := apkbuild.NewApkbuildFile(packageName, resp.Body)

	parsedApkBuild, err := apkbuild.Parse(apkbuildFile, nil)

	if err != nil {
		return errors.Wrapf(err, "failed to parse apkbuild %s", apkbuildURL)
	}

	c.ApkConvertors[packageName] = ApkConvertor{
		Apkbuild: &parsedApkBuild,
		GeneratedMelageConfig: &GeneratedMelageConfig{
			GeneratedFromComment: apkbuildURL,
			Package: build.Package{
				Epoch: 0,
			},
		},
	}
	c.OrderedKeys = append(c.OrderedKeys, packageName)
	//apkbuild := c.ApkConvertors[packageName].ApkBuild
	return nil
}

// recursively add dependencies, and their dependencies to our map
func (c Context) buildMapOfDependencies(apkBuildURI, pkgName string) error {

	convertor, exists := c.ApkConvertors[pkgName]
	if !exists {
		return fmt.Errorf("no top level apk convertor found for URI %s", apkBuildURI)
	}

	// recursively loop round and add any missing dependencies to the map
	for _, makeDep := range convertor.Apkbuild.Makedepends {

		dep := makeDep.Pkgname
		if strings.TrimSpace(dep) == "" {
			continue
		}

		// skip if we already have a package in wolfi-os repository
		wolfiPackage := c.WolfiOSPackages[dep]
		if len(wolfiPackage) > 0 {
			continue
		}

		// remove -dev from dependency name when looking up matching APKBUILD
		dep = strings.TrimSuffix(dep, "-dev")

		// using the same base URI switch the existing package name for the dependency and get related APKBUILD
		dependencyApkBuildURI := strings.ReplaceAll(apkBuildURI, convertor.Apkbuild.Pkgname, dep)

		// if we don't already have this dependency in the map, go get it
		_, exists = c.ApkConvertors[dep]
		if !exists {

			err := c.getApkBuildFile(dependencyApkBuildURI, dep)
			if err != nil {
				// log and skip this dependency if there's an issue getting the APKBUILD as we are guessing the location of the APKBUILD
				c.Logger.Printf("failed to get APKBUILD %s", dependencyApkBuildURI)
				continue
			}

			err = c.buildMapOfDependencies(dependencyApkBuildURI, dep)
			if err != nil {
				return errors.Wrap(err, "building map of dependencies")
			}

		}
	}
	return nil
}

// add pipeline fetch steps and validate checksums
func (c Context) buildFetchStep(converter ApkConvertor) error {

	apkBuild := converter.Apkbuild

	if len(apkBuild.Source) == 0 {
		c.Logger.Printf("skip adding pipeline for package %s, no source URL found", converter.Pkgname)
		return nil
	}
	if apkBuild.Pkgver == "" {
		return fmt.Errorf("no package version")
	}

	// there can be multiple sources, let's add them all so it's easier for users to remove from generated files if not needed
	for _, source := range apkBuild.Source {

		// replace typical placeholders found in APKBUILD files
		location := strings.ReplaceAll(source.Location, "$pkgver", apkBuild.Pkgver)
		location = strings.ReplaceAll(location, "${pkgver%.*}", apkBuild.Pkgver)
		location = strings.ReplaceAll(location, "$_pkgver", apkBuild.Pkgver)
		location = strings.ReplaceAll(location, "${_pkgver}", apkBuild.Pkgver)
		location = strings.ReplaceAll(location, "${pkgver//./-}", strings.ReplaceAll(apkBuild.Pkgver, ".", "-"))
		location = strings.ReplaceAll(location, "$pkgname", apkBuild.Pkgname)
		location = strings.ReplaceAll(location, "$_pkgname", apkBuild.Pkgname)
		location = strings.ReplaceAll(location, "${pkgname}", apkBuild.Pkgname)

		_, err := url.ParseRequestURI(location)
		if err != nil {
			return errors.Wrapf(err, "parsing URI %s", location)
		}

		req, _ := http.NewRequest("GET", location, nil)
		resp, err := c.Client.Do(req)

		if err != nil {
			return errors.Wrapf(err, "failed getting URI %s", location)
		}
		defer resp.Body.Close()

		failed := false
		if resp.StatusCode != http.StatusOK {
			c.Logger.Printf("non ok http response for URI %s code: %v", location, resp.StatusCode)
			failed = true
		}

		// todo
		// sha512.New()

		var expectedSha string
		if !failed {
			h := sha256.New()
			if _, err := io.Copy(h, resp.Body); err != nil {
				return errors.Wrapf(err, "generating sha265 for %s", location)
			}
			expectedSha = fmt.Sprintf("%x", h.Sum(nil))
		} else {
			expectedSha = "FIXME - SOURCE URL NOT VALID"
		}

		pipeline := build.Pipeline{
			Uses: "fetch",
			With: map[string]string{
				"uri":             strings.ReplaceAll(location, apkBuild.Pkgver, "${{package.version}}"),
				"expected-sha256": expectedSha,
			},
		}
		converter.GeneratedMelageConfig.Pipeline = append(converter.GeneratedMelageConfig.Pipeline, pipeline)
	}

	return nil
}

// maps APKBUILD values to melange
func (c ApkConvertor) mapMelange() {

	c.GeneratedMelageConfig.Package.Name = c.Apkbuild.Pkgname
	c.GeneratedMelageConfig.Package.Description = c.Apkbuild.Pkgdesc
	c.GeneratedMelageConfig.Package.Version = c.Apkbuild.Pkgver
	c.GeneratedMelageConfig.Package.TargetArchitecture = c.Apkbuild.Arch

	copyright := build.Copyright{
		Paths:       []string{"*"},
		Attestation: "TODO",
		License:     c.Apkbuild.License,
	}
	c.GeneratedMelageConfig.Package.Copyright = append(c.GeneratedMelageConfig.Package.Copyright, copyright)

	//switch c.Apkbuild.BuilderType {
	//
	//case BuilderTypeCMake:
	//	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "cmake/configure"})
	//	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "cmake/build"})
	//	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "cmake/install"})
	//
	//case BuilderTypeMeson:
	//	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "meson/configure"})
	//	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "meson/compile"})
	//	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "meson/install"})
	//
	//case BuilderTypeMake:
	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "autoconf/configure"})
	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "autoconf/make"})
	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "autoconf/make-install"})

	//default:
	//	c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "# FIXME"})
	//
	//}

	for _, subPackage := range c.Apkbuild.Subpackages {
		subpackage := build.Subpackage{
			Name: strings.Replace(subPackage.Subpkgname, "$pkgname", c.Apkbuild.Pkgname, 1),
		}

		// generate subpackages based on the subpackages defined in the APKBUILD
		var ext string
		parts := strings.Split(subPackage.Subpkgname, "-")
		if len(parts) == 2 {
			switch parts[1] {
			case "doc":
				ext = "manpages"
			case "dev":
				ext = "dev"
				subpackage.Dependencies = build.Dependencies{
					Runtime: []string{c.Apkbuild.Pkgname},
				}
			case "locales":
				ext = "dev"
			default:
				// if we don't recognise the extension make it obvious user needs to manually fix the config
				ext = "FIXME"
			}

			subpackage.Pipeline = []build.Pipeline{{Uses: "split/" + ext}}
			subpackage.Description = c.Apkbuild.Pkgname + " " + ext

		} else {
			// if we don't recognise the extension make it obvious user needs to manually fix the melange config
			subpackage.Pipeline = []build.Pipeline{{Runs: "FIXME"}}
		}

		c.GeneratedMelageConfig.Subpackages = append(c.GeneratedMelageConfig.Subpackages, subpackage)
	}
}

// adds a melange environment section
func (c ApkConvertor) buildEnvironment(additionalRepositories, additionalKeyrings []string) {

	env := apkotypes.ImageConfiguration{
		Contents: struct {
			Repositories []string
			Keyring      []string
			Packages     []string
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

	env.Contents.Repositories = append(env.Contents.Repositories, additionalRepositories...)
	env.Contents.Keyring = append(env.Contents.Keyring, additionalKeyrings...)
	for _, makedepend := range c.Apkbuild.Makedepends {
		env.Contents.Packages = append(env.Contents.Packages, makedepend.Pkgname)
	}

	for _, dependsDev := range c.Apkbuild.DependsDev {
		d := dependsDev.Pkgname
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

func (c ApkConvertor) write(orderNumber, outdir string) error {

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

	// write the melange config, prefix with our guessed order along with zero to help users easily rename / reorder generated files
	melangeFile := filepath.Join(outdir, orderNumber+"0-"+c.Apkbuild.Pkgname+".yaml")
	f, err := os.Create(melangeFile)
	if err != nil {
		return errors.Wrapf(err, "creating file %s", melangeFile)
	}
	defer f.Close()

	_, err = f.WriteString(fmt.Sprintf("# Generated from %s\n", c.GeneratedMelageConfig.GeneratedFromComment))
	if err != nil {
		return errors.Wrapf(err, "creating writing to file %s", melangeFile)
	}

	_, err = f.WriteString(string(actual))
	if err != nil {
		return errors.Wrapf(err, "creating writing to file %s", melangeFile)
	}
	return nil
}
