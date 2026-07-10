package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os/exec"
	"strconv"
	"strings"
)

func readFile(path string) []string {
	dat, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatalln(err)
	}

	return strings.Split(string(dat), "\n")
}

func writeFile(path string, data string) {
	err := ioutil.WriteFile(path, []byte(data), 0644)
	if err != nil {
		log.Fatalln(err)
	}
}

func bumpVersion(version string) string {
	version = strings.Trim(version[1:], "\n")
	prev, err := strconv.Atoi(version)
	if err != nil {
		log.Panicf("Failed to convert to int: %v", err)
	}
	return "v" + strconv.Itoa(prev+1)
}

func run(cmd string, opts ...interface{}) string {
	// opts
	silent := false

	// parse opts
	for i, opt := range opts {
		switch i {
		case 0:
			silent = opt.(bool)
		}
	}

	args := strings.Split(cmd, " ")
	cmd, args = args[0], args[1:]

	cmdOutput := &bytes.Buffer{}

	proc := exec.Command(cmd, args...)
	proc.Stdout = cmdOutput
	proc.Stderr = cmdOutput

	if err := proc.Run(); err != nil {
		log.Fatalf("Failed to run %s, %v\n%s", cmd, err, string(cmdOutput.Bytes()))
	}

	out := string(cmdOutput.Bytes())
	if !silent && out != "" {
		fmt.Print(out)
	}

	return out
}

func main() {
	version := bumpVersion(run("git describe --abbrev=0 --tags", true))
	run("git tag " + version)
	run("git push origin master --tags")
	run("git push -f origin master:production")
}
