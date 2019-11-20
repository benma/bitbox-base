// Package hsm contains the API to talk to the BitBoxBase HSM. The HSM is a platform+edition flavour
// of the BitBox02 firmware. There is an HSM bootloader and firmware; communication happens with
// usart framing over a serial port.
package hsm

import (
	"io"
	"time"

	"github.com/davecgh/go-spew/spew"
	bb02bootloader "github.com/digitalbitbox/bitbox02-api-go/api/bootloader"
	"github.com/digitalbitbox/bitbox02-api-go/api/common"
	bb02firmware "github.com/digitalbitbox/bitbox02-api-go/api/firmware"
	"github.com/digitalbitbox/bitbox02-api-go/communication/usart"
	"github.com/digitalbitbox/bitbox02-api-go/util/errp"
	"github.com/digitalbitbox/bitbox02-api-go/util/semver"
	"github.com/tarm/serial"
)

const (
	firmwareCMD   = 0x80 + 0x40 + 0x01
	bootloaderCMD = 0x80 + 0x40 + 0x03

	// attempting to connect to the UART serial port for this long before we give up.
	establishUARTTimeoutSeconds = 30
	// waiting this long for the firmware or bootloader to boot, after UART communication is
	// established.
	bootTimeoutSeconds = 30
)

// HSM lets you interact with the BitBox02-in-the-BitBoxBase (bootloader and firmware).
type HSM struct {
	serialPort string

	conn io.ReadWriteCloser
}

// NewHSM tries to connect to either a bootloader or firmware on the specified serial port.
// If none are found, and error is returned.
func NewHSM(serialPort string) *HSM {
	return &HSM{
		serialPort: serialPort,
	}
}

func (hsm *HSM) waitForCommunication() error {
	if hsm.conn != nil {
		return nil
	}

	for i := 0; i < establishUARTTimeoutSeconds; i++ {
		conn, err := serial.OpenPort(&serial.Config{
			Name:        hsm.serialPort,
			Baud:        115200,
			ReadTimeout: 5 * time.Second,
		})
		if err == nil {
			hsm.conn = conn
			return nil
		}
		time.Sleep(time.Second)
	}
	return errp.New("could not connect to the serial port after 30 attempts")
}

// getFirmware returns a firmware API instance, with the pairing/handshake already processed.
func (hsm *HSM) getFirmware() (*bb02firmware.Device, error) {
	if err := hsm.waitForCommunication(); err != nil {
		return nil, err
	}
	device := bb02firmware.NewDevice(
		// version and product infered via OP_INFO
		nil, nil,
		&bitbox02Config{},
		&usartCommunication{usart.NewCommunication(hsm.conn, firmwareCMD)},
		&bitbox02Logger{},
	)
	if err := device.Init(); err != nil {
		return nil, err
	}
	status := device.Status()
	switch status {
	case bb02firmware.StatusUnpaired:
		// expected, proceed below.
	case bb02firmware.StatusRequireAppUpgrade:
		return nil, errp.New("firmware unsupported, update of the BitBoxBase (middleware) is required")
	case bb02firmware.StatusPairingFailed:
		return nil, errp.New("device was expected to autoconfirm the pairing")
	default:
		return nil, errp.Newf("unexpected status: %v ", status)
	}
	// autoconfirm pairing on the host
	device.ChannelHashVerify(true)
	return device, nil
}

func (hsm *HSM) getBootloader() (*bb02bootloader.Device, error) {
	if err := hsm.waitForCommunication(); err != nil {
		return nil, err
	}

	return bb02bootloader.NewDevice(
		// hardcoded version for now, in the future could be `nil` with autodetection using
		// OP_INFO
		semver.NewSemVer(1, 0, 1),
		common.ProductBitBoxBaseStandard,
		&usartCommunication{usart.NewCommunication(hsm.conn, bootloaderCMD)},
		func(status *bb02bootloader.Status) {
			spew.Dump("status changed:", status)
		},
	), nil
}

func (hsm *HSM) isFirmware() (bool, error) {
	hsm.waitForCommunication()
	communication := &usartCommunication{usart.NewCommunication(hsm.conn, bootloaderCMD)}
	_, err := communication.Query([]byte{0x76})
	if err == usart.ErrEndpointUnavailable {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

// waitForBootloader returns a bootloader to use. If the HSM is booted into the firmware, we try to
// reboot into the bootloader first. After a certain timeout, an error is returned.
func (hsm *HSM) waitForBootloader() (*bb02bootloader.Device, error) {
	for second := 0; second < 2*bootTimeoutSeconds; second++ {
		isFirmware, err := hsm.isFirmware()
		if err != nil {
			return nil, err
		}
		if !isFirmware {
			return hsm.getBootloader()
		}
		firmware, err := hsm.getFirmware()
		if err != nil {
			return nil, err
		}
		// This is simply the reboot command.
		if err := firmware.UpgradeFirmware(); err != nil {
			return nil, err
		}
		// hsm.conn.Close()
		// hsm.conn = nil

		// Need to sleep here once, otherwise the next `isFirmware()` fake query call will timeout
		// after 5s with EOF (unsure why it doesn't respond earlier), delaying the reboot
		// noticably. A quick sleep here fixes this for now.
		time.Sleep(500 * time.Millisecond)

	}
	return nil, errp.New("waiting for bootloader timed out")
}

// wiatForFirmware returns a firmware to use. If the HSM is booted into the bootloader, we try to
// rebot into the firmware fist. After a certain timeout, an error is returned.
func (hsm *HSM) waitForFirmware() (*bb02firmware.Device, error) {
	for second := 0; second < bootTimeoutSeconds; second++ {
		isFirmware, err := hsm.isFirmware()
		if err != nil {
			return nil, err
		}
		if isFirmware {
			return hsm.getFirmware()
		}

		bootloader, err := hsm.getBootloader()
		if err != nil {
			return nil, err
		}
		// This is simply the reboot command.
		if err := bootloader.Reboot(); err != nil {
			return nil, err
		}
		time.Sleep(time.Second)
	}
	return nil, errp.New("waiting for firmware timed out")
}

// InteractWithBootloader lets you talk to the bootloader, rebooting into it from the firmware first
// if necessary. Returns an error if we fail to connect to it.
func (hsm *HSM) InteractWithBootloader(f func(*bb02bootloader.Device)) error {
	device, err := hsm.waitForBootloader()
	if err != nil {
		return err
	}

	f(device)
	return nil
}

// InteractWithFirmware lets you talk to the firmware, rebooting into it from the bootloader first
// if necessary. Returns an error if we fail to connect to it.
func (hsm *HSM) InteractWithFirmware(f func(*bb02firmware.Device)) error {
	device, err := hsm.waitForFirmware()
	if err != nil {
		return err
	}

	f(device)
	return nil
}
