// Copyright 2022 Chainguard, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

import (
	"chainguard.dev/melange/pkg/convert"
	"context"
	"fmt"
	"github.com/pkg/errors"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

type options struct {
	outDir                 string
	baseURIFormat          string
	additionalRepositories []string
	additionalKeyrings     []string
}

func ApkBuild() *cobra.Command {
	o := &options{}
	cmd := &cobra.Command{
		Use:     "apkbuild",
		Short:   "Converts an APKBUILD package into a melange.yaml",
		Long:    `Converts an APKBUILD package into a melange.yaml.`,
		Example: `  melange convert apkbuild libx11`,
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {

			if len(args) != 1 {
				return errors.New("too many arguments, expected only 1")
			}

			return o.ApkBuildCmd(cmd.Context(), args[0])
		},
	}

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	cmd.Flags().StringVar(&o.outDir, "out-dir", filepath.Join(cwd, "generated"), "directory where melange config will be output")
	cmd.Flags().StringVar(&o.baseURIFormat, "base-uri-format", "https://git.alpinelinux.org/aports/plain/main/%s/APKBUILD", "URI to use for querying APKBUILD for provided package name")
	cmd.Flags().StringArrayVar(&o.additionalRepositories, "additional-repositories", []string{}, "additional repositories to be added to melange environment config")
	cmd.Flags().StringArrayVar(&o.additionalKeyrings, "additional-keyrings", []string{}, "additional repositories to be added to melange environment config")

	return cmd
}

func (o options) ApkBuildCmd(ctx context.Context, packageName string) error {
	context, err := convert.New()
	if err != nil {
		return errors.Wrap(err, "initialising convert command")
	}

	context.AdditionalRepositories = o.additionalRepositories
	context.AdditionalKeyrings = o.additionalKeyrings
	context.OutDir = o.outDir

	configFilename := fmt.Sprintf(o.baseURIFormat, packageName)

	context.Logger.Printf("generating melange config files for APKBUILD %s", configFilename)

	err = context.Generate(configFilename, packageName)
	if err != nil {
		return errors.Wrap(err, "generating melange configuration")
	}

	return nil
}
