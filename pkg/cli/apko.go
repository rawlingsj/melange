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
	"github.com/pkg/errors"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func ApkBuild() *cobra.Command {

	var outDir string

	cmd := &cobra.Command{
		Use:     "apkbuild",
		Short:   "Converts an APKBUILD file into a melange.yaml",
		Long:    `Converts an APKBUILD file into a melange.yaml.`,
		Example: `  melange convert apkbuild https://foo.com/releases/APKBUILD`,
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {

			if len(args) != 1 {
				return errors.New("too many arguments, expected only 1")
			}

			return ApkBuildCmd(cmd.Context(), outDir, args[0])
		},
	}

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	cmd.Flags().StringVar(&outDir, "out-dir", filepath.Join(cwd, "generated"), "directory where melange config will be output")

	return cmd
}

func ApkBuildCmd(ctx context.Context, outDir, configFilename string) error {
	context, err := convert.New(configFilename, outDir)
	if err != nil {
		return errors.Wrap(err, "initialising convert command")
	}

	err = context.Generate()
	if err != nil {
		return errors.Wrap(err, "generating melange configuration")
	}

	return nil
}
