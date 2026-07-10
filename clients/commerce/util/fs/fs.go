package fs

import (
	"io/ioutil"
	"log"
	"strings"
)

func ReadFile(path string) []byte {
	dat, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatalln(err)
	}

	return dat
}

func SplitLines(s string) []string {
	return strings.Split(s, "\n")
}

func ReadLines(path string) []string {
	return SplitLines(string(ReadFile(path)))
}

func WriteFile(path string, data string) {
	err := ioutil.WriteFile(path, []byte(data), 0644)
	if err != nil {
		log.Fatalln(err)
	}
}
