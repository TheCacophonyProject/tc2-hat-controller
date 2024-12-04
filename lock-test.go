package main

import (
	"log"
	"os"
	"syscall"
	"time"
)

func main() {
	log.Println("Starting lock test")
	// Open serial file.
	serialFile, err := os.OpenFile("./foo", os.O_RDWR, 0666)
	if err != nil {
		log.Println(err)
	}

	defer func() {
		//serialFile.Close()
	}()

	// Try to get a lock on the serial file.
	err = syscall.Flock(int(serialFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		log.Println("Lock acquired")

		//return
	}

	if errno, ok := err.(syscall.Errno); ok && errno == syscall.EWOULDBLOCK {
		log.Println("Lock already acquired on that file")
	} else {
		log.Println(err)
	}
	time.Sleep(10 * time.Second)
}
