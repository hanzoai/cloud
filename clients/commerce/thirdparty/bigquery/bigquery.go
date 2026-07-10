package bigquery

import "context"

// "google.golang.org/api/bigquery/v2"

type Row map[string]interface{}
type Fields map[string]string

type Client struct {
	// Service *bigquery.Service
	Context context.Context
}

// func connect(c context.Context) (s *bigquery.Service, e error) {
// 	if client, err := serviceaccount.NewClient(c, bigquery.BigqueryScope); err != nil {
// 		return nil, fmt.Errorf("%s", err.Error())
// 	} else {
// 		if service, err := bigquery.New(client); err != nil {
// 			return nil, fmt.Errorf("%s", err.Error())
// 		} else {
// 			return service, nil
// 		}
// 	}
// }

func NewClient(c context.Context) (*Client, error) {
	return &Client{}, nil
	// if s, err := connect(c); err != nil {
	// 	return nil, err
	// } else {
	// 	client := &Client{
	// 		Service: s,
	// 		Context: c,
	// 	}
	// 	return client, nil
	// }
}

// InsertNewTable creates a new empty table for the given project and dataset with the field name/types defined in the fields map
func (c *Client) InsertNewTable(projectID, datasetID, tableName string, fields Fields) error {
	return nil
	// // build the table schema
	// schema := &bigquery.TableSchema{}
	// for k, v := range fields {
	// 	schema.Fields = append(schema.Fields, &bigquery.TableFieldSchema{Name: k, Type: v})
	// }

	// // build the table to insert
	// table := &bigquery.Table{}
	// table.Schema = schema

	// tr := &bigquery.TableReference{}
	// tr.DatasetId = datasetID
	// tr.ProjectId = projectID
	// tr.TableId = tableName

	// table.TableReference = tr

	// _, err := c.Service.Tables.Insert(projectID, datasetID, table).Do()
	// if err != nil {
	// 	return err
	// }

	// return nil
}

func (c *Client) InsertRows(projectID string, datasetID string, tableID string, rowsData []Row) error {
	return nil
	// rows := make([]*bigquery.TableDataInsertAllRequestRows, len(rowsData))
	// for i := 0; i < len(rowsData); i++ {
	// 	r := rowsData[i]
	// 	jsonRow := make(map[string]bigquery.JsonValue)
	// 	for key, value := range r {
	// 		jsonRow[key] = bigquery.JsonValue(value)
	// 	}
	// 	rows[i] = new(bigquery.TableDataInsertAllRequestRows)
	// 	rows[i].Json = jsonRow
	// }

	// insertRequest := &bigquery.TableDataInsertAllRequest{Rows: rows}

	// result, err := c.Service.Tabledata.InsertAll(projectID, datasetID, tableID, insertRequest).Do()
	// log.Warn("Result %#v", result, c.Context)
	// if err != nil {
	// 	return fmt.Errorf("Error inserting row: %v", err)
	// }

	// if len(result.InsertErrors) > 0 {
	// 	errstr := fmt.Sprintf("Insertion for %d rows failed\n", len(result.InsertErrors))
	// 	for _, errors := range result.InsertErrors {
	// 		for _, errorproto := range errors.Errors {
	// 			errstr += fmt.Sprintf("Error inserting row %d: %+v\n", errors.Index, errorproto)
	// 		}
	// 	}
	// 	return fmt.Errorf(errstr)
	// }
	// return nil
}
