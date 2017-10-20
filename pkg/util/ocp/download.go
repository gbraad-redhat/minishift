/*
Copyright (C) 2017 Red Hat, Inc.

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

package ocp

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/minishift/minishift/pkg/util/archive"
	minishiftos "github.com/minishift/minishift/pkg/util/os"
)

const (
	DMURL = "https://developers.redhat.com/download-manager/jdf/file/"
	OMURL = "https://mirror.openshift.com/pub/openshift-v3/clients/"
	TAR   = "tar.gz"
	ZIP   = "zip"
)

func DownloadOcpVersionFromMirror(version string, osType minishiftos.OS, outputPath string) error {
	platform := osType
	if osType == minishiftos.DARWIN {
		platform = "macosx"
	}

	extension := TAR
	if osType == minishiftos.WINDOWS {
		extension = ZIP
	}

	binaryName := "oc"
	// 3.7.0-0.153.0/windows/oc.zip
	url := fmt.Sprintf("%s%s", OMURL, fmt.Sprintf("%s/%s/%s.%s", strings.TrimPrefix(version, "v"), platform, binaryName, extension))
	targetFile := fmt.Sprintf("%s.%s", binaryName, extension)

	// Create target directory and file
	tmpDir, err := ioutil.TempDir("", "minishift-asset-download-")
	if err != nil {
		return errors.New("Cannot create temporary download directory.")
	}
	defer os.RemoveAll(tmpDir)

	// Create a tmp directory for the asset
	assetTmpFile := filepath.Join(tmpDir, targetFile)

	// Download the actual file
	if _, err := download(url, assetTmpFile, "", ""); err != nil {
		return err
	}

	// TODO: Extract into a function
	binaryPath := ""
	switch {
	case strings.HasSuffix(assetTmpFile, TAR):
		// unzip
		tarFile := assetTmpFile[:len(assetTmpFile)-3]
		err = archive.Ungzip(assetTmpFile, tarFile)
		if err != nil {
			return errors.New("Cannot ungzip")
		}

		// untar
		err = archive.Untar(tarFile, tmpDir)
		if err != nil {
			return errors.New("Cannot untar")
		}

		content, err := listDirExcluding(tmpDir, ".*.tar.*")
		if err != nil {
			return errors.New("Cannot list content of")
		}
		if len(content) > 1 {
			return errors.New(fmt.Sprintf("Unexpected number of files in tmp directory: %s", content))
		}

		//binaryPath = filepath.Join(tmpDir, content[0])
		binaryPath = tmpDir
	case strings.HasSuffix(assetTmpFile, ZIP):
		//contentDir := assetTmpFile[:len(assetTmpFile)-4]
		err = archive.Unzip(assetTmpFile, tmpDir)
		if err != nil {
			return errors.New("Cannot unzip")
		}
		binaryPath = tmpDir
	}

	if osType == minishiftos.WINDOWS {
		binaryName = binaryName + ".exe"
	}
	binaryPath = filepath.Join(binaryPath, binaryName)

	// Copy the requested asset into its final destination
	err = os.MkdirAll(outputPath, 0755)
	if err != nil && !os.IsExist(err) {
		return errors.New("Cannot create the target directory.")
	}

	finalBinaryPath := filepath.Join(outputPath, binaryName)
	err = copy(binaryPath, finalBinaryPath)
	if err != nil {
		return err
	}

	err = os.Chmod(finalBinaryPath, 0777)
	if err != nil {
		return fmt.Errorf("Cannot make executable: %s", err.Error())
	}

	return nil
}

func DownloadOcpVersionFromRHDev(username string, password string, version string, platform string, filepath string) {

	// check platform
	if platform == "darwin" {
		platform = "macosx"
	}

	extension := TAR
	if platform == "windows" {
		extension = ZIP
	}

	// oc-3.5.5.31.24-windows.zip
	url := fmt.Sprintf("%s%s?workflow=direct", DMURL, fmt.Sprintf("oc-%s-%s.%s", version, platform, extension))
	target := fmt.Sprintf("%s/%s.%s", filepath, "oc", extension)
	/*_, err := */ download(url, target, username, password)
}

func listDirExcluding(dir string, excludeRegexp string) ([]string, error) {
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	result := []string{}
	for _, f := range files {
		matched, err := regexp.MatchString(excludeRegexp, f.Name())
		if err != nil {
			return nil, err
		}

		if !matched {
			result = append(result, f.Name())
		}

	}

	return result, nil
}

func copy(src, dest string) error {
	srcFile, err := os.Open(src)
	defer srcFile.Close()
	if err != nil {
		return fmt.Errorf("Cannot open src file: %s", src)
	}

	destFile, err := os.Create(dest)
	defer destFile.Close()
	if err != nil {
		return fmt.Errorf("Cannot create dst file: %s", dest)
	}

	_, err = io.Copy(destFile, srcFile)
	if err != nil {
		return fmt.Errorf("Cannot copy: %s", err.Error())
	}

	err = destFile.Sync()
	if err != nil {
		return fmt.Errorf("Cannot copy: %s", err.Error())
	}

	return nil
}

func download(url string, filename string, username string, password string) (bool, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, fmt.Errorf("Error in request: '%s'", err.Error())
	}
	if len(username) > 0 {
		req.SetBasicAuth(username, password)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("Unable to get resource '%s': %s", url, err.Error())
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("Wrong credentials or filename '%s': %d", url, resp.StatusCode)
	}

	defer func() { _ = resp.Body.Close() }()
	out, err := os.Create(filename)
	defer out.Close()
	if err != nil {
		return false, fmt.Errorf("Not able to create file as '%s': %s", url, err.Error())
	}
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return false, fmt.Errorf("Not able to copy file to '%s': %s", filename, err.Error())
	}

	return true, nil
}
