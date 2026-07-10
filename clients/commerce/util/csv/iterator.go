package csv

import (
	"encoding/csv"
	"os"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

type Record struct {
	Index int
	Row   []string
}

func Iterator(filename string) <-chan Record {
	ch := make(chan Record)

	go func() {
		file, err := os.Open(filename)
		defer file.Close()
		if err != nil {
			log.Fatal("Failed to open CSV File: %v", err)
		}

		reader := csv.NewReader(file)
		reader.FieldsPerRecord = -1

		// Skip header
		reader.Read()

		for i := 0; true; i++ {
			// Loop until exhausted
			row, err := reader.Read()

			// Break on error
			if err != nil {
				break
			}

			ch <- Record{i, row}
		}
		close(ch)
	}()

	return ch
}
