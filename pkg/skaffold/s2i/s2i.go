/*
Copyright 2018 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package s2i

import (
	"os"
	"path/filepath"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/schema/v1alpha2"
)

type S2IDependencyResolver struct{}

func (*S2IDependencyResolver) GetDependencies(a *v1alpha2.Artifact) ([]string, error) {
	// Walk the workspace and add everything
	var files []string
	walkErr := filepath.Walk(a.Workspace, func(fpath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(a.Workspace, fpath)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, relPath)
		}
		return nil
	})
	return files, walkErr
}
