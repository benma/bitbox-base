// Package main provides the entry point into the middleware and accepts command line arguments.
// Once compiled, the application pipes information from bitbox-base backend services to the bitbox-wallet-app and serves as an authenticator to the bitbox-base.
package main

import (
	"flag"
	"log"
	"net/http"

	middleware "github.com/digitalbitbox/bitbox-base/middleware/src"
	"github.com/digitalbitbox/bitbox-base/middleware/src/handlers"
)

// version defines the middleware version
// The version is upgraded via semantic versioning
const version string = "0.0.1"

func main() {
	hsmtest := flag.Bool("hsmtest", false, "For NickeZ and conte91 to experiment with the HSM Go lib")

	middlewarePort := flag.String("middlewareport", "8845", "Port the middleware should listen on (default 8845)")
	electrsRPCPort := flag.String("electrsport", "51002", "Electrs rpc port")
	dataDir := flag.String("datadir", ".base", "Directory where middleware persistent data like noise keys is stored")
	network := flag.String("network", "testnet", "Indicate wether running bitcoin on testnet or mainnet")
	bbbConfigScript := flag.String("bbbconfigscript", "/opt/shift/scripts/bbb-config.sh", "Path to the bbb-config file that allows setting system configuration")
	bbbCmdScript := flag.String("bbbcmdscript", "/opt/shift/scripts/bbb-cmd.sh", "Path to the bbb-cmd file that allows executing system commands")
	bbbSystemctlScript := flag.String("bbbsystemctlscript", "/opt/shift/scripts/bbb-systemctl.sh", "Path to the bbb-systemctl script that allows starting and stopping services on the Base.")
	prometheusURL := flag.String("prometheusurl", "http://localhost:9090", "Url of the prometheus server in the form of 'http://localhost:9090'")
	redisPort := flag.String("redisport", "6379", "Port of the Redis server")
	redisMock := flag.Bool("redismock", false, "Mock redis for development instead of connecting to a redis server, default is 'false', use 'true' as an argument to mock")
	imageUpdateInfoURL := flag.String("updateinfourl", "https://shiftcrypto.ch/updates/base.json", "URL to query information about updates from (defaults to https://shiftcrypto.ch/updates/base.json)")
	notificationNamedPipePath := flag.String("notificationNamedPipePath", "/tmp/middleware-notification.pipe", "notificationNamedPipe specifies the path where the Middleware creates a named pipe to receive notifications from other processes on the BitBoxBase (defaults to /tmp/middleware-notification.pipe)")
	flag.Parse()

	if *hsmtest {
		hsmTest()
		return
	}

	argumentMap := make(map[string]string)
	argumentMap["middlewarePort"] = *middlewarePort
	argumentMap["electrsRPCPort"] = *electrsRPCPort
	argumentMap["network"] = *network
	argumentMap["bbbConfigScript"] = *bbbConfigScript
	argumentMap["bbbCmdScript"] = *bbbCmdScript
	argumentMap["bbbSystemctlScript"] = *bbbSystemctlScript
	argumentMap["prometheusURL"] = *prometheusURL
	argumentMap["redisPort"] = *redisPort
	argumentMap["imageUpdateInfoURL"] = *imageUpdateInfoURL
	argumentMap["notificationNamedPipePath"] = *notificationNamedPipePath
	argumentMap["middlewareVersion"] = version

	logBeforeExit := func() {
		// Recover from all panics and log error before panicking again.
		if r := recover(); r != nil {
			// r is of type interface{}, just print its value
			log.Printf("%v, error detected, shutting down.", r)
			panic(r)
		}
	}
	defer logBeforeExit()

	middleware, err := middleware.NewMiddleware(argumentMap, *redisMock)
	if err != nil {
		log.Fatalf("error starting the middleware: %s . Is redis connected? \nIf you are running the middleware outside of the base consider setting the redis mock flag to true: '-redismock true' .", err.Error())
	}
	log.Println("--------------- Started middleware --------------")

	handlers := handlers.NewHandlers(middleware, *dataDir)
	log.Printf("Binding middleware api to port %s\n", *middlewarePort)

	if err := http.ListenAndServe(":"+*middlewarePort, handlers.Router); err != nil {
		log.Println(err.Error() + " Failed to listen for HTTP")
	}
}
