package main

import (
	"flag"
	"log"
	"os"
)

func main() {
	socket := flag.String("socket", "", "unix socket path")
	data := flag.String("data", "", "state directory")
	flag.Parse()
	if *socket == "" || *data == "" {
		log.Fatal("usage: fakedocker -socket PATH -data DIR")
	}
	st, err := Open(*data)
	if err != nil {
		log.Fatal(err)
	}
	ln, err := Listen(*socket)
	if err != nil {
		log.Fatal(err)
	}
	if err := Serve(ln, st); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}
