package wolfios

import (
	"encoding/xml"
	"github.com/pkg/errors"
	"regexp"
	"strings"
)

type Context struct {
	WolfiPackages map[string][]string
}
type WolfiPackage struct {
	Repository string
	Key        string
}

type ListBucketResult struct {
	Contents []Contents `xml:"Contents"`
}
type Contents struct {
	Key string `xml:"Key"`
}

func ParseWolfiPackages(data []byte) (map[string][]string, error) {

	c := Context{
		WolfiPackages: make(map[string][]string),
	}

	wolfiData := ListBucketResult{}
	err := xml.Unmarshal(data, &wolfiData)
	if err != nil {
		return c.WolfiPackages, errors.Wrap(err, "unmarshalling wolfi-os data")
	}

	for _, contents := range wolfiData.Contents {

		err := c.parseKey(contents.Key)
		if err != nil {
			return c.WolfiPackages, errors.Wrapf(err, "failed to parse key %s", contents.Key)

		}
	}
	return c.WolfiPackages, nil
}

func (c Context) parseKey(key string) error {

	if strings.HasSuffix(key, "wolfi-signing.rsa.pub") {
		return nil
	}
	if strings.HasSuffix(key, "APKINDEX.tar.gz") {
		return nil
	}

	lastInd := strings.LastIndex(key, "/")
	packageName := c.getPackageName(key[lastInd+1:])
	//w := WolfiPackage{
	//	Key: key,
	//	Repository:
	//}
	c.WolfiPackages[packageName] = append(append(c.WolfiPackages[packageName], key))

	return nil

}

func (c Context) getPackageName(name string) string {
	r := regexp.MustCompile("([+-]?(=\\.\\d|\\d)(?:\\d+)?(?:\\.?\\d*))(?:[eE]([+-]?\\d+))?([+-]?(=\\.\\d|\\d)(?:\\d+)?(?:\\.?\\d*))(?:[eE]([+-]?\\d+))?-r[0-9]+\\.[a-zA-Z]+")
	parts := r.Split(name, -1)

	return parts[0]
}
