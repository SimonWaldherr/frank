package main

import (
	"encoding/gob"
	"errors"
	"flag"
	"fmt"
	"github.com/cmceniry/frank"
	"github.com/cmceniry/golokia"
	"net"
	"os"
	"regexp"
	"time"
)

type MyConfig struct {
	Host		string
	Port		int
	Keyspace	string
	ColumnFamily	string
	Operation	string
	Destination	string
}

var config = MyConfig{}

var (
	ErrHistConvert     = errors.New("Did not convert correctly")
	ErrHistLenMismatch = errors.New("Did not return the right number of values")
	ErrHistEleConvert  = errors.New("Did not convert element correctly")
)

var gclient = golokia.NewClient("localhost", "7025")

func getHistogram(ks, cf, field string) ([]float64, error) {
	bean := fmt.Sprintf("columnfamily=%s,keyspace=%s,type=ColumnFamilies", cf, ks)
	lrlhm, err := gclient.GetAttr("org.apache.cassandra.db", bean, field)
	if err != nil {
		return nil, err
	}
	l, ok := lrlhm.([]interface{})
	if !ok {
		//fmt.Printf("%v\n", lrlhm)
		return nil, ErrHistConvert
	}
	if len(l) != len(frank.Labels) {
		//fmt.Printf("%d\n%d\n", len(l), len(frank.Labels))
		return nil, ErrHistLenMismatch
	}
	ret := make([]float64, len(l))
	for idx, val := range l {
		if valF, ok := val.(float64); ok {
			ret[idx] = valF
		} else {
			return nil, ErrHistEleConvert
		}
	}
	return ret, nil
}

func collect(keyspace string, columnfamily string, operation string, sink chan frank.NamedSample) {
	if res, err := getHistogram(keyspace, columnfamily, operation); err != nil {
		fmt.Printf("Error in collector(%s,%s,%s): %s\n", keyspace, columnfamily, operation, err)
	} else {
		name := "localhost/" + keyspace + "/" + columnfamily + "/" + operation
		select {
		case sink <- frank.NamedSample{frank.Sample{time.Now().UnixNano()/1e6, res}, name}:
			// Normal behavior
		default:
			// Otherwise, don't do anything with the data since we don't want to block
		}
	}
}

func forwarder(src chan frank.NamedSample, dst string) {
	for {
		conn, err := net.Dial("tcp", dst)
		if err != nil {
			fmt.Printf("Error in forwawrder: %s\n", err)
			time.Sleep(5 * time.Second)
			continue
		}
		defer conn.Close()
		enc := gob.NewEncoder(conn)
		pointsSent := 0
		for {
			err := enc.Encode(<-src)
			if err != nil {
				fmt.Printf("Error in forwawrder: %s\n", err)
				time.Sleep(5 * time.Second)
				break
			}
			pointsSent += 1
			if pointsSent % 100 == 0 {
				fmt.Printf("%d data points sent\n", pointsSent)
			}
		}
	}
}

func getCFs() [][]string {
	re := regexp.MustCompile("columnfamily=([a-zA-Z0-9]+),keyspace=([a-zA-Z0-9]+),type=ColumnFamilies")

	ret := [][]string{}
	beans, err := gclient.ListBeans("org.apache.cassandra.db")
	if err != nil {
		panic(err)
	}
	for _, bean := range beans {
		r := re.FindStringSubmatch(bean)
		if r != nil {
			ret = append(ret, []string{r[2], r[1]})
		}
	}
	return ret
}

func main() {
	var op string
	flag.StringVar(&config.Host, "host", "localhost", "Jolokia Host to connect to")
	flag.IntVar(&config.Port, "port", 7025, "Jolokia Port to connect to")
	flag.StringVar(&config.Keyspace, "keyspace", "Keyspace1", "Keyspace to connect to")
	flag.StringVar(&config.ColumnFamily, "cf", "Standard1", "ColumnFamily to get metrics for")
	flag.StringVar(&op, "op", "Write", "Type of operation to get metrics for: Read or Write")
	flag.StringVar(&config.Destination, "destination", "127.0.0.1:4271", "TCP IP:Port to forward results to")
	flag.Parse()

	switch op {
	case "Write":
		config.Operation = "LifetimeWriteLatencyHistogramMicros"
	case "Read":
		config.Operation = "LifetimeReadLatencyHistogramMicros"
	default:
		fmt.Println("Operation must be Read or Write")
		os.Exit(-1)
	}

	stream := make(chan frank.NamedSample)
	cfs := getCFs()
	go func() {
		for _ = range time.Tick(5 * time.Second) {
			for _, cf := range cfs {
				collect(cf[0], cf[1], "LifetimeWriteLatencyHistogramMicros", stream)
				collect(cf[0], cf[1], "LifetimeReadLatencyHistogramMicros", stream)
			}
		}
	}()
	go forwarder(stream, config.Destination)

	for {
		time.Sleep(100 * time.Second)
	}
}
