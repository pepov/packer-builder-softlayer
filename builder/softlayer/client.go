package softlayer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"text/template"
	"time"
)

const SOFTLAYER_API_URL = "api.softlayer.com/rest/v3"

type SoftlayerClient struct {
	// The http client for communicating
	http *http.Client

	// Credentials
	user   string
	apiKey string
}

// Based on: http://sldn.softlayer.com/reference/datatypes/SoftLayer_Container_Virtual_Guest_Configuration/
type InstanceType struct {
	HostName             string
	Domain               string
	Datacenter           string
	Cpus                 int
	Memory               int64
	HourlyBillingFlag    bool
	LocalDiskFlag        bool
	DiskCapacity         int
	NetworkSpeed         int
	ProvisioningSshKeyId float64
	BaseImageId          string
	BaseOsCode           string
}

func (self SoftlayerClient) New(user string, key string) *SoftlayerClient {
	return &SoftlayerClient{
		http: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
		},
		user:   user,
		apiKey: key,
	}
}

func (self SoftlayerClient) generateRequestBody(templatePath string, templateData interface{}) *bytes.Buffer {
	cwd, _ := os.Getwd()
	bodyTemplate := template.Must(template.ParseFiles(filepath.Join(cwd, templatePath)))
	body := new(bytes.Buffer)
	bodyTemplate.Execute(body, templateData)

	log.Printf("Generated request body %s", body)

	return body
}

func (self SoftlayerClient) doRawHttpRequest(path string, requestType string, requestBody *bytes.Buffer) ([]byte, error) {
	url := fmt.Sprintf("https://%s:%s@%s/%s", self.user, self.apiKey, SOFTLAYER_API_URL, path)
	log.Printf("Sending new request to softlayer: %s", url)

	// Create the request object
	var lastResponse http.Response
	switch requestType {
	case "POST", "DELETE":
		req, err := http.NewRequest(requestType, url, requestBody)

		if err != nil {
			return nil, err
		}
		resp, err := self.http.Do(req)

		if err != nil {
			return nil, err
		} else {
			lastResponse = *resp
		}
	case "GET":
		resp, err := http.Get(url)

		if err != nil {
			return nil, err
		} else {
			lastResponse = *resp
		}
	default:
		return nil, errors.New(fmt.Sprintf("Undefined request type '%s', only GET/POST/DELETE are available!", requestType))
	}

	responseBody, err := ioutil.ReadAll(lastResponse.Body)
	lastResponse.Body.Close()
	if err != nil {
		return nil, err
	}

	log.Printf("Received response from SoftLayer: %s", responseBody)
	return responseBody, nil
}

func (self SoftlayerClient) doHttpRequest(path string, requestType string, requestBody *bytes.Buffer) (map[string]interface{}, error) {
	responseBody, err := self.doRawHttpRequest(path, requestType, requestBody)
	if err != nil {
		err := errors.New(fmt.Sprintf("Failed to get proper HTTP response from SoftLayer API"))
		return nil, err
	}

	var decodedResponse map[string]interface{}
	err = json.Unmarshal(responseBody, &decodedResponse)
	if err != nil {
		err := errors.New(fmt.Sprintf("Failed to decode JSON response from SoftLayer: %s | %s", responseBody, err))
		return nil, err
	}

	return decodedResponse, nil
}

func (self SoftlayerClient) CreateInstance(instance InstanceType) (map[string]interface{}, error) {
	// SoftLayer API puts some limitations on hostname and domain fields of the request
	validName, err := regexp.Compile("[^A-Za-z0-9\\-\\.]+")
	if err != nil {
		return nil, err
	}

	instance.HostName = validName.ReplaceAllString(instance.HostName, "")
	instance.Domain = validName.ReplaceAllString(instance.Domain, "")

	requestBody := self.generateRequestBody("builder/softlayer/templates/virtual_guest/createObject.json", instance)
	data, err := self.doHttpRequest("SoftLayer_Virtual_Guest/createObject", "POST", requestBody)
	if err != nil {
		return nil, nil
	}

	return data, err
}

func (self SoftlayerClient) DestroyInstance(instanceId string) error {
	response, err := self.doRawHttpRequest(fmt.Sprintf("SoftLayer_Virtual_Guest/%s.json", instanceId), "DELETE", new(bytes.Buffer))

	log.Printf("Deleted an Instance with id (%s), response: %s", instanceId, response)
	// Process response for success?

	return err
}

func (self SoftlayerClient) UploadSshKey(label string, publicKey string) (keyId float64, err error) {
	templateRawData := map[string]string{"PublicKey": publicKey, "Label": label}
	requestBody := self.generateRequestBody("builder/softlayer/templates/security_ssh_key/createObject.json", templateRawData)
	data, err := self.doHttpRequest("SoftLayer_Security_Ssh_Key/createObject", "POST", requestBody)
	if err != nil {
		return 0, nil
	}

	return data["id"].(float64), err
}

func (self SoftlayerClient) DestroySshKey(keyId float64) error {
	response, err := self.doRawHttpRequest(fmt.Sprintf("SoftLayer_Security_Ssh_Key/%v.json", int(keyId)), "DELETE", new(bytes.Buffer))

	log.Printf("Deleted an SSH Key with id (%v), response: %s", keyId, response)
	// Process response for success?

	return err
}

func (self SoftlayerClient) getInstancePublicIp(instanceId string) (string, error) {
	response, err := self.doRawHttpRequest(fmt.Sprintf("SoftLayer_Virtual_Guest/%s/getPrimaryIpAddress.json", instanceId), "GET", nil)
	if err != nil {
		return "", nil
	}

	var validIp = regexp.MustCompile(`[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}`)
	ipAddress := validIp.Find(response)

	return string(ipAddress), nil
}

func (self SoftlayerClient) captureImage(instanceId string, imageName string, imageDescription string) (map[string]interface{}, error) {
	templateRawData := map[string]string{"ImageDescription": imageDescription, "ImageName": imageName}
	requestBody := self.generateRequestBody("builder/softlayer/templates/virtual_guest/captureImage.json", templateRawData)
	data, err := self.doHttpRequest(fmt.Sprintf("SoftLayer_Virtual_Guest/%s/captureImage.json", instanceId), "POST", requestBody)
	if err != nil {
		return nil, nil
	}

	return data, err
}

func (self SoftlayerClient) destroyImage(imageId string, datacenterName string) error {
	response, err := self.doRawHttpRequest(fmt.Sprintf("SoftLayer_Virtual_Guest/%s.json", imageId), "DELETE", new(bytes.Buffer))

	log.Printf("Deleted an image with id (%s), response: %s", imageId, response)
	// Process response for success?

	return err
}

func (self SoftlayerClient) isInstantsReady(instanceId string) (bool, error) {
	powerData, err := self.doHttpRequest(fmt.Sprintf("SoftLayer_Virtual_Guest/%s/getPowerState.json", instanceId), "GET", nil)
	if err != nil {
		return false, nil
	}
	isPowerOn := powerData["keyName"].(string) == "RUNNING"

	transactionData, err := self.doHttpRequest(fmt.Sprintf("SoftLayer_Virtual_Guest/%s/getActiveTransaction.json", instanceId), "GET", nil)
	if err != nil {
		return false, nil
	}
	noTransactions := len(transactionData) == 0

	return isPowerOn && noTransactions, err
}

func (self SoftlayerClient) waitForInstanceReady(instanceId string, timeout time.Duration) error {
	done := make(chan struct{})
	defer close(done)

	result := make(chan error, 1)
	go func() {
		attempts := 0
		for {
			attempts += 1

			//log.Printf("Checking instance status... (attempt: %d)", attempts)
			isReady, err := self.isInstantsReady(instanceId)
			if err != nil {
				result <- err
				return
			}

			if isReady {
				result <- nil
				return
			}

			// Wait 3 seconds in between
			time.Sleep(3 * time.Second)

			// Verify we shouldn't exit
			select {
			case <-done:
				// We finished, so just exit the goroutine
				return
			default:
				// Keep going
			}
		}
	}()

	log.Printf("Waiting for up to %d seconds for instance to become ready", timeout/time.Second)
	select {
	case err := <-result:
		return err
	case <-time.After(timeout):
		err := fmt.Errorf("Timeout while waiting to for the instance to become ready")
		return err
	}
}