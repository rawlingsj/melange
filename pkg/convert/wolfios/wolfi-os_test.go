package wolfios

import (
	"encoding/xml"
	"github.com/stretchr/testify/assert"
	"os"
	"path/filepath"
	"testing"
)

func TestWolfiOSMap_UnmarshalXML(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "wolfios-data.xml"))
	assert.NoError(t, err)

	wolfiData := ListBucketResult{}
	err = xml.Unmarshal(data, &wolfiData)
	assert.NoError(t, err)

	key := wolfiData.Contents[0].Key
	assert.Equal(t, key, "bootstrap/stage1/x86_64/cross-libstdc++-stage1-12.2.0-r2.apk")

}

func TestWolfiOSLarge_Parse(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "wolfios-data-large.xml"))
	assert.NoError(t, err)

	wolfi, err := ParseWolfiPackages(data)
	assert.NoError(t, err)

	exists := wolfi["sln"]
	assert.Equalf(t, []string{"os/x86_64/sln-2.36-r0.apk"}, exists, "sln not found")

	exists = wolfi["readline-dev"]
	assert.Equalf(t, []string{"os/x86_64/readline-dev-8.1.2-r1.apk"}, exists, "readline-dev not found")

	exists = wolfi["python3"]
	assert.Equalf(t, []string{"bootstrap/stage3/packages/x86_64/python3-3.10.6-r0.apk", "bootstrap/stage3/x86_64/python3-3.10.6-r0.apk", "os/x86_64/python3-3.10.7-r0.apk"}, exists, "readline-dev not found")

}
