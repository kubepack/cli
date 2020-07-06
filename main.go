package main

import (
	"kubepack.dev/cli/pkg"
	"math/rand"
	"time"

	"github.com/appscode/go/log"
	_ "k8s.io/client-go/kubernetes/fake"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"kmodules.xyz/client-go/logs"
)

func main() {
	rand.Seed(time.Now().UnixNano())
	logs.InitLogs()
	defer logs.FlushLogs()

	if err := pkg.NewRootCmd().Execute(); err != nil {
		log.Fatalln("error:", err)
	}
}
