// Copyright 2018 Mikhail Klementev. All rights reserved.
// Use of this source code is governed by a AGPLv3 license
// (or later) that can be found in the LICENSE file.

package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strings"

	"github.com/jollheef/out-of-tree/config"
)

func kernelListHandler(kcfg config.KernelConfig) (err error) {
	if len(kcfg.Kernels) == 0 {
		return errors.New("No kernels found")
	}
	for _, k := range kcfg.Kernels {
		fmt.Println(k.DistroType, k.DistroRelease, k.KernelRelease)
	}
	return
}

func matchDebianKernelPkg(container, mask string, generic bool) (pkgs []string,
	err error) {

	cmd := "apt-cache search linux-image | cut -d ' ' -f 1"
	c := dockerCommand(container, "/tmp", "1m", cmd)
	rawOutput, err := c.CombinedOutput()
	if err != nil {
		return
	}

	r, err := regexp.Compile("linux-image-" + mask)
	if err != nil {
		return
	}

	kernels := r.FindAll(rawOutput, -1)

	for _, k := range kernels {
		pkg := string(k)
		if generic && !strings.HasSuffix(pkg, "generic") {
			continue
		}
		pkgs = append(pkgs, pkg)
	}

	return
}

func dockerImagePath(sk config.KernelMask) (path string, err error) {
	usr, err := user.Current()
	if err != nil {
		return
	}

	path = usr.HomeDir + "/.out-of-tree/"
	path += sk.DistroType.String() + "/" + sk.DistroRelease
	return
}

func generateBaseDockerImage(sk config.KernelMask) (err error) {
	imagePath, err := dockerImagePath(sk)
	if err != nil {
		return
	}
	dockerPath := imagePath + "/Dockerfile"

	d := "# BASE\n"

	if exists(dockerPath) {
		log.Printf("Base image for %s:%s found",
			sk.DistroType.String(), sk.DistroRelease)
		return
	} else {
		log.Printf("Base image for %s:%s not found, start generating",
			sk.DistroType.String(), sk.DistroRelease)
		os.MkdirAll(imagePath, os.ModePerm)
	}

	d += fmt.Sprintf("FROM %s:%s\n",
		strings.ToLower(sk.DistroType.String()),
		sk.DistroRelease,
	)

	switch sk.DistroType {
	case config.Ubuntu:
		d += "ENV DEBIAN_FRONTEND=noninteractive\n"
		d += "RUN apt-get update\n"
		d += "RUN apt-get install -y build-essential libelf-dev\n"
		d += "RUN apt-get install -y wget git\n"
	default:
		s := fmt.Sprintf("%s not yet supported", sk.DistroType.String())
		err = errors.New(s)
		return
	}

	d += "# END BASE\n\n"

	err = ioutil.WriteFile(dockerPath, []byte(d), 0644)
	if err != nil {
		return
	}

	cmd := exec.Command("docker", "build", "-t", sk.DockerName(), imagePath)
	rawOutput, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Base image for %s:%s generating error, see log",
			sk.DistroType.String(), sk.DistroRelease)
		log.Println(string(rawOutput))
		return
	}

	log.Printf("Base image for %s:%s generating success",
		sk.DistroType.String(), sk.DistroRelease)

	return
}

func dockerImageAppend(sk config.KernelMask, pkgname string) (err error) {
	imagePath, err := dockerImagePath(sk)
	if err != nil {
		return
	}

	raw, err := ioutil.ReadFile(imagePath + "/Dockerfile")
	if err != nil {
		return
	}

	if strings.Contains(string(raw), pkgname) {
		// already installed kernel
		log.Printf("kernel %s for %s:%s is already exists",
			pkgname, sk.DistroType.String(), sk.DistroRelease)
		return
	}

	log.Printf("Start adding kernel %s for %s:%s",
		pkgname, sk.DistroType.String(), sk.DistroRelease)

	//s := fmt.Sprintf("RUN apt-get install -y %s %s\n", pkgname,
	s := fmt.Sprintf("RUN apt-get install -y %s %s\n", pkgname,
		strings.Replace(pkgname, "image", "headers", -1))

	err = ioutil.WriteFile(imagePath+"/Dockerfile",
		append(raw, []byte(s)...), 0644)
	if err != nil {
		return
	}

	cmd := exec.Command("docker", "build", "-t", sk.DockerName(), imagePath)
	rawOutput, err := cmd.CombinedOutput()
	if err != nil {
		// Fallback to previous state
		werr := ioutil.WriteFile(imagePath+"/Dockerfile", raw, 0644)
		if werr != nil {
			return
		}

		log.Printf("Add kernel %s for %s:%s error, see log",
			pkgname, sk.DistroType.String(), sk.DistroRelease)
		log.Println(string(rawOutput))
		return
	}

	log.Printf("Add kernel %s for %s:%s success",
		pkgname, sk.DistroType.String(), sk.DistroRelease)

	return
}

func kickImage(name string) (err error) {
	cmd := exec.Command("docker", "run", name, "bash", "-c", "ls")
	_, err = cmd.CombinedOutput()
	return
}

func copyKernels(name string) (err error) {
	cmd := exec.Command("docker", "ps", "-a")
	rawOutput, err := cmd.CombinedOutput()
	if err != nil {
		log.Println(string(rawOutput))
		return
	}

	r, err := regexp.Compile(".*" + name)
	if err != nil {
		return
	}

	var containerID string

	what := r.FindAll(rawOutput, -1)
	for _, w := range what {
		containerID = strings.Fields(string(w))[0]
		break
	}

	usr, err := user.Current()
	if err != nil {
		return
	}

	target := usr.HomeDir + "/.out-of-tree/kernels/"
	if !exists(target) {
		os.MkdirAll(target, os.ModePerm)
	}

	cmd = exec.Command("docker", "cp", containerID+":/boot/.", target)
	rawOutput, err = cmd.CombinedOutput()
	if err != nil {
		log.Println(string(rawOutput))
		return
	}

	return
}

func kernelAutogenHandler(kcfg config.KernelConfig, workPath string) (err error) {
	ka, err := config.ReadArtifactConfig(workPath + "/.out-of-tree.toml")
	if err != nil {
		return
	}

	var usedImages []string

	for _, sk := range ka.SupportedKernels {
		if sk.DistroRelease == "" {
			err = errors.New("Please set distro_release")
			return
		}

		err = generateBaseDockerImage(sk)
		if err != nil {
			return
		}

		var pkgs []string
		pkgs, err = matchDebianKernelPkg(sk.DockerName(),
			sk.ReleaseMask, true)
		if err != nil {
			return
		}

		for _, pkg := range pkgs {
			dockerImageAppend(sk, pkg)
		}

		usedImages = append(usedImages, sk.DockerName())
	}

	for _, ui := range usedImages {
		err = kickImage(ui)
		if err != nil {
			log.Println("kick image", ui, ":", err)
			continue
		}

		err = copyKernels(ui)
		if err != nil {
			log.Println("copy kernels", ui, ":", err)
			continue
		}
	}

	log.Println("Currently generation of kernels.toml is not implemented")
	log.Println("So next step is up to you hand :)")
	return
}
