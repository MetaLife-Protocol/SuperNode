package main

import (
	"fmt"

	"github.com/MetaLife-Protocol/SuperNode/cmd/supernode/mainimpl"
)

func main() {
	if _, err := mainimpl.StartMain(); err != nil {
		fmt.Printf("quit with err %s\n", err)
	}
}
