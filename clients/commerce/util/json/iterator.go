package json

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
)

type Record struct {
	Data  string
	Index int
}

func Iterator(filename string) <-chan Record {
	ch := make(chan Record)

	go func() {
		file, err := os.Open(filename)
		defer file.Close()
		if err != nil {
			log.Fatal(fmt.Sprintf("Failed to open JSON File: %v", err))
		}

		r := bufio.NewReader(file)
		line := 1
		for {
			data, err := r.ReadString(10) // 0x0A separator = newline
			ch <- Record{data, line}
			if err == io.EOF {
				// do something here
				ch <- Record{data, line}
				break
			} else if err != nil {
				log.Fatal(fmt.Sprintf("Failed to open JSON File: %v", err))
			}

			line = line + 1
		}
		close(ch)
	}()

	return ch
}
