package main

import (
	"bufio"
	"fmt"
	"encoding/hex"
	"io"
	"log"
	"os"
	"strings"

	"github.com/digitalbitbox/bitbox02-api-go/api/bootloader"
	"github.com/digitalbitbox/bitbox02-api-go/api/common"
	"github.com/digitalbitbox/bitbox02-api-go/api/firmware"
	//"github.com/digitalbitbox/bitbox02-api-go/communication/u2fhid"
	"github.com/digitalbitbox/bitbox02-api-go/communication/usart"
	"github.com/digitalbitbox/bitbox02-api-go/util/semver"
	"github.com/flynn/noise"
	"github.com/karalabe/usb"
	"github.com/tarm/serial"
)

func getBitBox02USB() io.ReadWriteCloser {
	isBitBox02 := func(deviceInfo usb.DeviceInfo) bool {
		const (
			bitbox02VendorID  = 0x03eb
			bitbox02ProductID = 0x2403
		)

		return deviceInfo.VendorID == bitbox02VendorID &&
			deviceInfo.ProductID == bitbox02ProductID &&
			(deviceInfo.UsagePage == 0xffff || deviceInfo.Interface == 0)
	}

	deviceInfos, err := usb.EnumerateHid(0, 0)
	if err != nil {
		panic(err)
	}
	for _, deviceInfo := range deviceInfos {
		if isBitBox02(deviceInfo) {
			device, err := deviceInfo.Open()
			if err != nil {
				panic(err)
			}
			return device
		}
	}
	panic("no bb02 found")
}

func getBitBox02UART() io.ReadWriteCloser {
	c := &serial.Config{Name: "/dev/ttyUSB0", Baud: 115200, Parity: 0}
	s, err := serial.OpenPort(c)
	if err != nil {
		panic(err)
	}
	return s
}

// See ConfigInterace: https://github.com/digitalbitbox/bitbox02-api-go/blob/e8ae46debc009cfc7a64f45ec191de0220f0c401/api/firmware/device.go#L50
type bitbox02Config struct{}

func (bb02Config *bitbox02Config) ContainsDeviceStaticPubkey(pubkey []byte) bool {
	return false
}
func (bb02Config *bitbox02Config) AddDeviceStaticPubkey(pubkey []byte) error {
	return nil
}
func (bb02Config *bitbox02Config) GetAppNoiseStaticKeypair() *noise.DHKey {
	return nil
}
func (bb02Config *bitbox02Config) SetAppNoiseStaticKeypair(key *noise.DHKey) error {
	return nil
}

type bitbox02Logger struct{}

func (bb02Logger *bitbox02Logger) Error(msg string, err error) {
	log.Println(msg, err)
}
func (bb02Logger *bitbox02Logger) Info(msg string) {
	log.Println(msg)
}
func (bb02Logger *bitbox02Logger) Debug(msg string) {
	log.Println(msg)
}

// just translating SendFrame with incompatible signature (string<->[]byte), will be made consistent
// later...
type usartCommunication struct {
	*usart.Communication
}

func (communication usartCommunication) SendFrame(msg string) error {
	return communication.Communication.SendFrame([]byte(msg))
}

func hsmFirmwareConnect(communication firmware.Communication) (*firmware.Device, error) {
	device := firmware.NewDevice(
		// hardcoded version for now, in the future can be `nil` with autodetection using OP_INFO
		semver.NewSemVer(4, 2, 2),
		// Edition also hardcoded for now, will need to convert the stuff to platform/edition tuples
		common.EditionStandard,
		&bitbox02Config{},
		communication,
		&bitbox02Logger{},
	)
	err := device.Init(false)
	return device, err;
}

func startFirmwareCli(communication firmware.Communication, device *firmware.Device) {
	fmt.Println("---------------------")
	fmt.Println(" BitboxBase Firmware ")
	fmt.Println("---------------------")

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("-> ")
	for scanner.Scan() {
		text := scanner.Text()
		text = strings.TrimSpace(text)
		switch (text) {
		case "bootloader":
			err := device.UpgradeFirmware()
			if err != nil {
				panic(err)
			}
			fmt.Print("Reboot successful. Bye!")
		case "help":
			fmt.Println("- bootloader: Reboot to bootloader")
			fmt.Println("- random: Requests a random number")
			fmt.Println("- quit: Quit")
		case "quit":
			return
		case "random":
			random, err := device.Random()
			if err != nil {
				panic(err)
			}
			fmt.Printf("Bitbox generated the following random number: %s\n", hex.EncodeToString(random))
		case "status":
			status := device.Status()
			fmt.Printf("Current status: %s\n", status)
		}
		fmt.Print("-> ")
	}
}

func startBootloaderCli(communication bootloader.Communication, device *bootloader.Device) {
	fmt.Println("---------------------")
	fmt.Println("BitboxBase Bootloader")
	fmt.Println("---------------------")

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("-> ")
	for scanner.Scan() {
		text := scanner.Text()
		text = strings.TrimSpace(text)
		switch (text) {
		case "hash":
			firmwareHash, _, err := device.GetHashes(false, false)
			if err != nil {
				panic(err)
			}
			fmt.Printf("firmware hash returned by bootloader: %x\n", firmwareHash)
		case "help":
			fmt.Println("- hash: Display the firmware hash")
			fmt.Println("- versions: Display signing key versions")
			fmt.Println("- quit: Quit")
		case "quit":
			return
		case "reboot":
			err := device.Reboot()
			if err != nil {
				panic(err)
			}
			fmt.Printf("Rebooted successfully. Bye!")
			return
		case "versions":
			v1, v2, err := device.Versions()
			if err != nil {
				panic(err)
			}
			fmt.Printf("Versions: %d %d\n", v1, v2)
		}
		fmt.Print("-> ")
	}
}


func hsmBootloaderTest(communication bootloader.Communication) {
	device := bootloader.NewDevice(
		// hardcoded version for now, in the future can be `nil` with autodetection using OP_INFO
		semver.NewSemVer(1, 0, 1),
		// Edition also hardcoded for now, will need to convert the stuff to platform/edition tuples
		common.EditionStandard,
		communication,
		func(status *bootloader.Status) {
			fmt.Println("status changed:", status)
		},
	)
	startBootloaderCli(communication, device)
}

func hsmTest() {

	const firmwareCMD = 0x80 + 0x40 + 0x01
	const bootloaderCMD = 0x80 + 0x40 + 0x03

	// open device (io.ReadWriteCloser interface with Read, Write, Close functions).
	// a) can be u2fhid/USB
	//deviceUSB := getBitBox02USB()
	//communication := u2fhid.NewCommunication(deviceUSB, cmd)
	// b) can be usart/Serial:
	deviceUART := getBitBox02UART()
	firmwareCommunication := usartCommunication{usart.NewCommunication(deviceUART, firmwareCMD)}
	firmwareDevice, err := hsmFirmwareConnect(firmwareCommunication)
	if (err == nil) {
		startFirmwareCli(firmwareCommunication, firmwareDevice)
	} else {
		// make sure to use bootloaderCMD above
		const cmd = bootloaderCMD
		communication := usartCommunication{usart.NewCommunication(deviceUART, cmd)}
		hsmBootloaderTest(communication)
	}
}
