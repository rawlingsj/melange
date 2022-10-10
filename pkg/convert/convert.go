package convert

import (
	"bufio"
	"bytes"
	apkotypes "chainguard.dev/apko/pkg/build/types"
	"chainguard.dev/melange/pkg/build"
	"chainguard.dev/melange/pkg/convert/wolfios"
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
	*ApkBuild
	ApkBuildRaw            []byte
	*GeneratedMelageConfig `yaml:"-"`
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
	Source         []string
	BuilderType    string
	Sha512sums     map[string]string
}
type GeneratedMelageConfig struct {
	Package              build.Package                `yaml:"package"`
	Environment          apkotypes.ImageConfiguration `yaml:"environment,omitempty"`
	Pipeline             []build.Pipeline             `yaml:"pipeline,omitempty"`
	Subpackages          []build.Subpackage           `yaml:"subpackages,omitempty"`
	GeneratedFromComment string                       `yaml:"-"`
}

const (
	BuilderTypeMake  = "make"
	BuilderTypeCMake = "cmake"
	BuilderTypeMeson = "meson"
)

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

func (c Context) Generate(apkBuildURI string) error {

	// get the contents of the APKBUILD file
	err := c.getApkBuildFile(apkBuildURI)
	if err != nil {
		return errors.Wrap(err, "getting apk build file")
	}

	// build map of dependencies
	err = c.buildMapOfDependencies(apkBuildURI)
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

// perform a basic parse of an APKBUILD file
func (c *Context) parseApkBuild(r io.Reader, key string) error {

	c.ApkConvertors[key] = ApkConvertor{
		ApkBuild: &ApkBuild{
			BuilderType: BuilderTypeMake,
			Sha512sums:  map[string]string{},
		},
		GeneratedMelageConfig: &GeneratedMelageConfig{
			GeneratedFromComment: key,
			Package: build.Package{
				Epoch: 0,
			},
		},
	}
	c.OrderedKeys = append(c.OrderedKeys, key)
	apkbuild := c.ApkConvertors[key].ApkBuild

	// turn into byte array else scanner skips lines
	b, err := io.ReadAll(r)
	if err != nil {
		return errors.Wrapf(err, "reading APKBUILD file")
	}

	scanner := bufio.NewScanner(strings.NewReader(string(b)))

	scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}

		if j := bytes.IndexAny(data, "\n"); j >= 0 {

			// if there is a quote then we should split on the end quote as could be multiline string
			if k := bytes.IndexAny(data[0:j+1], "\""); k >= 0 {
				last := bytes.IndexAny(data[k+1:], "\"")
				if last == -1 {
					last = len(data)
				}

				// replace newlines
				result := bytes.ReplaceAll(data[0:k+last+2], []byte("\n"), []byte(" "))

				// replace tabs
				result = bytes.ReplaceAll(result, []byte("\t"), []byte(""))

				// replace double spaces with single to help with split
				result = bytes.ReplaceAll(result, []byte("  "), []byte(" "))
				result = bytes.TrimSpace(result)
				return k + last + 2, result, nil
			}
			return j + 1, data[0 : j+1], nil
		}

		if atEOF {
			return len(data), data, nil
		}

		return 0, nil, nil
	})

	for scanner.Scan() {

		line := strings.TrimSpace(scanner.Text())
		if line != "" && strings.Contains(line, "=") {
			parts := strings.Split(line, "=")

			value := strings.TrimSpace(parts[1])

			value = strings.ReplaceAll(value, "\"", "")
			//value = strings.TrimSpace(value)

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
				if strings.IndexAny(value, "\n") >= 0 {
					apkbuild.Arch = append(apkbuild.Arch, strings.Split(value, "\n")...)
				} else {
					apkbuild.Arch = strings.Split(value, " ")
				}
			case "license":
				apkbuild.License = value
			case "depends_dev":
				if strings.IndexAny(value, "\n") >= 0 {
					apkbuild.DependDev = append(apkbuild.DependDev, strings.Split(value, "\n")...)
				} else {
					apkbuild.DependDev = strings.Split(value, " ")
				}
			case "subpackages":

				if strings.IndexAny(value, "\n") >= 0 {
					apkbuild.SubPackages = append(apkbuild.SubPackages, strings.Split(value, "\n")...)
				} else {
					apkbuild.SubPackages = strings.Split(value, " ")
				}
			case "makedepends":
				if strings.IndexAny(value, "\n") >= 0 {
					apkbuild.MakeDepends = append(apkbuild.MakeDepends, strings.Split(value, "\n")...)
				} else {
					apkbuild.MakeDepends = append(apkbuild.MakeDepends, strings.Split(value, " ")...)
				}

			case "makedepends_host":
				if strings.IndexAny(value, "\n") >= 0 {
					apkbuild.MakeDepends = append(apkbuild.MakeDepends, strings.Split(value, "\n")...)
				} else {
					apkbuild.MakeDepends = append(apkbuild.MakeDepends, strings.Split(value, " ")...)
				}

			case "source":

				multiparts := strings.Split(value, " ")
				if len(multiparts) > 0 {
					for _, multipart := range multiparts {
						if strings.TrimSpace(multipart) != "" {
							apkbuild.Source = append(apkbuild.Source, multipart)
						}
					}

				} else {
					apkbuild.Source = append(apkbuild.Source, strings.Split(value, " ")...)
				}
			case "sha512sums":

				multiparts := strings.Split(value, " ")
				if len(multiparts) > 0 {
					for i := 0; i < len(multiparts); i++ {
						sha := multiparts[i]
						if strings.TrimSpace(sha) != "" {

							// checksum details are in key / value format
							// this is pretty horrible code, let's make this better
							artifact := multiparts[i+1]
							if strings.TrimSpace(artifact) != "" {
								i++

							} else {
								artifact = multiparts[i+2]
								if artifact != "" {
									i = i + 2
								}
							}
							if artifact == "" {
								c.Logger.Printf("checksum %s does not have an artifact", sha)
								continue
							}

							apkbuild.Sha512sums[artifact] = sha
						}
					}

				} else {
					checksumDetails := strings.Split(value, " ")
					if len(checksumDetails) != 2 {
						c.Logger.Printf("checksum does not have 2 parts to it, expecting sha and filename, got %s", checksumDetails)
						continue
					}

					apkbuild.Sha512sums[checksumDetails[0]] = checksumDetails[1]
				}
				//todo we don't yet parse lines with make / cmake / meson in
			case "make":
				apkbuild.BuilderType = BuilderTypeMake
			case "cmake":
				apkbuild.BuilderType = BuilderTypeCMake
			case "meson":
				apkbuild.BuilderType = BuilderTypeMeson

			}
		}
	}

	return nil
}

func (c *Context) split(value string, apkbuild *ApkBuild, foo string) {
	if strings.IndexAny(value, "\n") > 0 {
		apkbuild.Arch = append(apkbuild.Arch, strings.Split(value, "\n")...)
	} else {
		apkbuild.Arch = strings.Split(value, " ")
	}
}

// recursively add dependencies, and their dependencies to our map
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

		c.Logger.Printf("looking at %s", dep)

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

// add pipeline fetch steps and validate checksums
func (c Context) buildFetchStep(converter ApkConvertor) error {

	apkBuild := converter.ApkBuild

	if len(apkBuild.Source) == 0 {
		c.Logger.Printf("skip adding pipeline for package %s, no source URL found", converter.PackageName)
		return nil
	}
	if apkBuild.PackageVersion == "" {
		return fmt.Errorf("no package version")
	}

	// there can be multiple sources, let's add them all so it's easier for users to remove from generated files if not needed
	for _, s := range apkBuild.Source {
		// replace typical placeholders found in APKBUILD files
		source := strings.ReplaceAll(s, "$pkgver", apkBuild.PackageVersion)
		source = strings.ReplaceAll(source, "${pkgver%.*}", apkBuild.PackageVersion)
		source = strings.ReplaceAll(source, "$_pkgver", apkBuild.PackageVersion)
		source = strings.ReplaceAll(source, "${_pkgver}", apkBuild.PackageVersion)
		source = strings.ReplaceAll(source, "${pkgver//./-}", strings.ReplaceAll(apkBuild.PackageVersion, ".", "-"))
		source = strings.ReplaceAll(source, "$pkgname", apkBuild.PackageName)
		source = strings.ReplaceAll(source, "$_pkgname", apkBuild.PackageName)
		source = strings.ReplaceAll(source, "${pkgname}", apkBuild.PackageName)

		_, err := url.ParseRequestURI(source)
		if err != nil {
			return errors.Wrapf(err, "parsing URI %s", source)
		}

		req, _ := http.NewRequest("GET", source, nil)
		resp, err := c.Client.Do(req)

		if err != nil {
			return errors.Wrapf(err, "failed getting URI %s", source)
		}
		defer resp.Body.Close()

		failed := false
		if resp.StatusCode != http.StatusOK {
			c.Logger.Printf("non ok http response for URI %s code: %v", source, resp.StatusCode)
			failed = true
		}

		// todo
		// sha512.New()

		var expectedSha string
		if !failed {
			h := sha256.New()
			if _, err := io.Copy(h, resp.Body); err != nil {
				return errors.Wrapf(err, "generating sha265 for %s", source)
			}
			expectedSha = fmt.Sprintf("%x", h.Sum(nil))
		} else {
			expectedSha = "FIXME - SOURCE URL NOT VALID"
		}

		pipeline := build.Pipeline{
			Uses: "fetch",
			With: map[string]string{
				"uri":             strings.ReplaceAll(source, apkBuild.PackageVersion, "${{package.version}}"),
				"expected-sha256": expectedSha,
			},
		}
		converter.GeneratedMelageConfig.Pipeline = append(converter.GeneratedMelageConfig.Pipeline, pipeline)
	}

	return nil
}

// maps APKBUILD values to melange
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

	switch c.ApkBuild.BuilderType {

	case BuilderTypeCMake:
		c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "cmake/configure"})
		c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "cmake/build"})
		c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "cmake/install"})

	case BuilderTypeMeson:
		c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "meson/configure"})
		c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "meson/compile"})
		c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "meson/install"})

	case BuilderTypeMake:
		c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "autoconf/configure"})
		c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "autoconf/make"})
		c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "autoconf/make-install"})

	default:
		c.GeneratedMelageConfig.Pipeline = append(c.GeneratedMelageConfig.Pipeline, build.Pipeline{Uses: "# FIXME"})

	}

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
	melangeFile := filepath.Join(outdir, orderNumber+"0-"+c.ApkBuild.PackageName+".yaml")
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
