package digitalocean

import (
	"errors"
	"fmt"
	"koding/kites/kloud/eventer"
	"koding/kites/kloud/klientprovisioner"
	"koding/kites/kloud/kloud/machinestate"
	"koding/kites/kloud/kloud/protocol"
	"koding/kites/kloud/packer"
	"koding/kites/kloud/sshutil"
	"koding/kites/kloud/utils"
	"net/url"
	"strconv"
	"time"

	klientprotocol "koding/kites/klient/protocol"

	kiteprotocol "github.com/koding/kite/protocol"
	"github.com/koding/logging"
	"github.com/mitchellh/mapstructure"
	"github.com/mitchellh/packer/builder/digitalocean"
)

const ProviderName = "digitalocean"

type pushFunc func(string, int)

type DigitalOcean struct {
	Client   *digitalocean.DigitalOceanClient
	Log      logging.Logger
	SignFunc func(string) (string, string, error)

	Creds struct {
		ClientID string `mapstructure:"clientId"`
		APIKey   string `mapstructure:"apiKey"`
	}

	Builder struct {
		DropletId   string `mapstructure:"instanceId"`
		DropletName string `mapstructure:"droplet_name" packer:"droplet_name"`

		Type     string `mapstructure:"type" packer:"type"`
		ClientID string `mapstructure:"client_id" packer:"client_id"`
		APIKey   string `mapstructure:"api_key" packer:"api_key"`

		RegionID uint `mapstructure:"region_id" packer:"region_id"`
		SizeID   uint `mapstructure:"size_id" packer:"size_id"`
		ImageID  uint `mapstructure:"image_id" packer:"image_id"`

		Region string `mapstructure:"region" packer:"region"`
		Size   string `mapstructure:"size" packer:"size"`
		Image  string `mapstructure:"image" packer:"image"`

		PrivateNetworking bool   `mapstructure:"private_networking" packer:"private_networking"`
		SnapshotName      string `mapstructure:"snapshot_name" packer:"snapshot_name"`
		SSHUsername       string `mapstructure:"ssh_username" packer:"ssh_username"`
		SSHPort           uint   `mapstructure:"ssh_port" packer:"ssh_port"`

		RawSSHTimeout   string `mapstructure:"ssh_timeout"`
		RawStateTimeout string `mapstructure:"state_timeout"`
	}
}

func (d *DigitalOcean) Name() string {
	return ProviderName
}

// Prepare prepares the state for upcoming methods like Build/etc.. It's needs to
// be called before every other method call once. Raws contains the credentials
// as a map[string]interface{} format.
func (d *DigitalOcean) Prepare(raws ...interface{}) (err error) {
	if len(raws) != 2 {
		return errors.New("need at least two arguments")
	}

	// Credentials
	if err := mapstructure.Decode(raws[0], &d.Creds); err != nil {
		return err
	}

	// Builder data
	if err := mapstructure.Decode(raws[1], &d.Builder); err != nil {
		return err
	}

	if d.Creds.ClientID == "" {
		return errors.New("credentials client_id is empty")
	}

	if d.Creds.APIKey == "" {
		return errors.New("credentials api_key is empty")
	}

	d.Builder.ClientID = d.Creds.ClientID
	d.Builder.APIKey = d.Creds.APIKey

	d.Client = digitalocean.DigitalOceanClient{}.New(d.Creds.ClientID, d.Creds.APIKey)

	// authenticate credentials with a simple call
	// TODO: cache gor a given clientID and apiKey
	d.Log.Debug("Testing authentication with a simple /regions call")
	_, err = d.Regions()
	if err != nil {
		return errors.New("authentication with DigitalOcean failed.")
	}

	return nil
}

func (d *DigitalOcean) pusher(opts *protocol.MachineOptions, state machinestate.State) pushFunc {
	return func(msg string, percentage int) {
		d.Log.Info("[machineId: '%s': username: '%s' dropletName: '%s' snapshotName: '%s'] - %s",
			opts.MachineId, opts.Username, opts.InstanceName, opts.ImageName, msg)

		opts.Eventer.Push(&eventer.Event{
			Message:    msg,
			Status:     state,
			Percentage: percentage,
		})
	}
}

// Build is building an image and creates a droplet based on that image. If the
// given snapshot/image exist it directly skips to creating the droplet. It
// acceps two string arguments, first one is the snapshotname, second one is
// the dropletName.
func (d *DigitalOcean) Build(opts *protocol.MachineOptions) (p *protocol.BuildResponse, err error) {
	if opts.ImageName == "" {
		return nil, errors.New("snapshotName is empty")
	}
	snapshotName := opts.ImageName

	if opts.InstanceName == "" {
		return nil, errors.New("dropletName is empty")
	}
	dropletName := opts.InstanceName

	if opts.Username == "" {
		return nil, errors.New("username is empty")
	}

	if opts.Eventer == nil {
		return nil, errors.New("Eventer is not defined.")
	}

	push := d.pusher(opts, machinestate.Building)

	// needed because this is passed as `data` to packer.Provider
	d.Builder.SnapshotName = snapshotName

	var image digitalocean.Image

	// check if snapshot image does exist, if not create a new one.
	push(fmt.Sprintf("Fetching image %s", snapshotName), 10)
	image, err = d.Image(snapshotName)
	if err != nil {
		push(fmt.Sprintf("Image %s does not exist, creating a new one", snapshotName), 12)
		image, err = d.CreateImage()
		if err != nil {
			return nil, err
		}

		defer func() {
			// return value of latest err, if there is no error just return lazily
			if err == nil {
				return
			}

			push("Destroying image", 95)
			err := d.DestroyImage(image.Id)
			if err != nil {
				curlstr := fmt.Sprintf("curl '%v/images/%d/destroy?client_id=%v&api_key=%v'",
					digitalocean.DIGITALOCEAN_API_URL, image.Id, d.Creds.ClientID, d.Creds.APIKey)

				push(fmt.Sprintf("Error cleaning up droplet. Please delete the droplet manually: %v", curlstr), 95)
			}
		}()
	}

	// create temporary key to deploy user based key
	push(fmt.Sprintf("Creating temporary ssh key"), 15)
	privateKey, publicKey, err := sshutil.TemporaryKey()
	if err != nil {
		return nil, err
	}

	// The name of the public key on DO
	name := fmt.Sprintf("koding-%d", time.Now().UTC().UnixNano())
	d.Log.Debug("Creating key with name '%s'", name)
	keyId, err := d.CreateKey(name, publicKey)
	if err != nil {
		return nil, err
	}

	defer func() {
		push("Destroying temporary droplet key", 95)
		err := d.DestroyKey(keyId) // remove after we are done
		if err != nil {
			curlstr := fmt.Sprintf("curl '%v/ssh_keys/%v/destroy?client_id=%v&api_key=%v'",
				digitalocean.DIGITALOCEAN_API_URL, keyId, d.Creds.ClientID, d.Creds.APIKey)

			push(fmt.Sprintf("Error cleaning up ssh key. Please delete the key manually: %v", curlstr), 95)
		}
	}()

	// now create a the machine based on our created image
	push(fmt.Sprintf("Creating droplet %s", dropletName), 20)
	dropletInfo, err := d.CreateDroplet(dropletName, keyId, image.Id)
	if err != nil {
		return nil, err
	}
	d.Builder.DropletId = strconv.Itoa(dropletInfo.Droplet.Id)

	defer func() {
		// return value of latest err, if there is no error just return lazily
		if err == nil {
			return
		}

		push("Destroying droplet", 95)
		err := d.DestroyDroplet(uint(dropletInfo.Droplet.Id))
		if err != nil {
			curlstr := fmt.Sprintf("curl '%v/droplets/%v/destroy?client_id=%v&api_key=%v'",
				digitalocean.DIGITALOCEAN_API_URL, dropletInfo.Droplet.Id, d.Creds.ClientID, d.Creds.APIKey)

			push(fmt.Sprintf("Error cleaning up droplet. Please delete the droplet manually: %v", curlstr), 95)
		}
	}()

	// Now we wait until it's ready, it takes approx. 50-70 seconds to finish,
	// but we also add a timeout  of five minutes to not let stuck it there
	// forever.
	if err := d.WaitUntilReady(dropletInfo.Droplet.EventId, 25, 59, push); err != nil {
		return nil, err
	}

	// our droplet has now an IP adress, get it
	push(fmt.Sprintf("Getting info about droplet"), 60)
	info, err := d.Info(opts)
	if err != nil {
		return nil, err
	}
	dropInfo := info.(Droplet)

	sshAddress := dropInfo.IpAddress + ":22"
	sshConfig, err := sshutil.SshConfig(privateKey)
	if err != nil {
		return nil, err
	}

	push(fmt.Sprintf("Connecting to ssh %s", sshAddress), 65)
	client, err := sshutil.ConnectSSH(sshAddress, sshConfig)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	// generate kite key specific for the user
	push("Creating kite.key", 70)
	kiteKey, kiteId, err := d.SignFunc(opts.Username)
	if err != nil {
		return nil, err
	}
	push(fmt.Sprintf("Kite key created for id %s", kiteId), 75)

	// for debugging, remove it later ...
	push(fmt.Sprintf("Writing kite key to temporary file (kite.key)"), 75)
	// DEBUG
	// if err := ioutil.WriteFile("kite.key", []byte(kiteKey), 0400); err != nil {
	// 	d.Log.Info("couldn't write temporary kite file", err)
	// }

	keyPath := "/opt/kite/klient/key/kite.key"

	push(fmt.Sprintf("Copying remote kite key %s", keyPath), 85)
	remoteFile, err := client.Create(keyPath)
	if err != nil {
		return nil, err
	}

	_, err = remoteFile.Write([]byte(kiteKey))
	if err != nil {
		return nil, err
	}

	push(fmt.Sprintf("Starting klient on remote machine"), 90)
	if err := client.StartCommand("service klient start"); err != nil {
		return nil, err
	}

	// arslan/public-host/klient/0.0.1/unknown/testkloud-1401755272229370184-0/393ff626-8fa5-4713-648c-4a51604f98c6
	klient := kiteprotocol.Kite{
		Username:    opts.Username, // kite.key is signed for this user
		ID:          kiteId,        // id is generated by ourself
		Hostname:    dropletName,   // hostname is the dropletName
		Name:        klientprotocol.Name,
		Environment: klientprotocol.Environment,
		Region:      klientprotocol.Region,
		Version:     klientprotocol.Version,
	}

	return &protocol.BuildResponse{
		QueryString:  klient.String(),
		IpAddress:    dropInfo.IpAddress,
		InstanceName: dropInfo.Name,
		InstanceId:   dropInfo.Id,
	}, nil
}

// CheckEvent checks the given eventID and returns back the result. It's useful
// for checking the status of an event. Usually it's called in a for/select
// statement and get polled.
func (d *DigitalOcean) CheckEvent(eventId int) (*Event, error) {
	path := fmt.Sprintf("events/%d", eventId)

	body, err := digitalocean.NewRequest(*d.Client, path, url.Values{})
	if err != nil {
		return nil, err
	}

	event := &Event{}
	if err := mapstructure.Decode(body, event); err != nil {
		return nil, err
	}

	return event, nil
}

// WaitUntilReady checks the given state for the eventID and returns nil if the
// state has been reached. It returns an error if the given timeout has been
// reached, if another generic error is produced or if the event status is of
// type "ERROR".
func (d *DigitalOcean) WaitUntilReady(eventId, from, to int, push pushFunc) error {
	for {
		select {
		case <-time.After(time.Minute):
			return errors.New("Timeout while waiting for droplet to become ready")
		case <-time.Tick(3 * time.Second):
			if push != nil {
				push("Waiting for droplet to be ready", from)
			}

			event, err := d.CheckEvent(eventId)
			if err != nil {
				return err
			}

			if event.Event.ActionStatus == "done" {
				push("Waiting is done. Got a successfull result.", from)
				return nil
			}

			// the next steps percentage is 60, fake it until we got there
			if from < to {
				from += 2
			}
		}
	}
}

// CreateKey creates a new ssh key with the given name and the associated
// public key. It returns a unique id that is associated with the given
// publicKey. This id is used to show, edit or delete the key.
func (d *DigitalOcean) CreateKey(name, publicKey string) (uint, error) {
	return d.Client.CreateKey(name, publicKey)
}

// DestroyKey removes the ssh key that is associated with the given id.
func (d *DigitalOcean) DestroyKey(id uint) error {
	return d.Client.DestroyKey(id)
}

// CreateImage creates an image using Packer. It uses digitalocean.Builder
// data. It returns the image info.
func (d *DigitalOcean) CreateImage() (digitalocean.Image, error) {
	data, err := utils.TemplateData(d.Builder, klientprovisioner.RawData)
	if err != nil {
		return digitalocean.Image{}, err
	}

	provider := &packer.Provider{
		BuildName: "digitalocean",
		Data:      data,
	}

	// this is basically a "packer build template.json"
	if err := provider.Build(); err != nil {
		return digitalocean.Image{}, err
	}

	// return the image result
	return d.Image(d.Builder.SnapshotName)
}

// CreateDroplet creates a new droplet with a hostname, key and image_id. It
// returns back the dropletInfo.
func (d *DigitalOcean) CreateDroplet(hostname string, keyId, image_id uint) (*DropletInfo, error) {
	params := url.Values{}
	params.Set("name", hostname)

	found_size, err := d.Client.Size(d.Builder.Size)
	if err != nil {
		return nil, fmt.Errorf("Invalid size or lookup failure: '%s': %s", d.Builder.Size, err)
	}

	found_region, err := d.Client.Region(d.Builder.Region)
	if err != nil {
		return nil, fmt.Errorf("Invalid region or lookup failure: '%s': %s", d.Builder.Region, err)
	}

	params.Set("size_slug", found_size.Slug)
	params.Set("image_id", strconv.Itoa(int(image_id)))
	params.Set("region_slug", found_region.Slug)
	params.Set("ssh_key_ids", fmt.Sprintf("%v", keyId))
	params.Set("private_networking", fmt.Sprintf("%v", d.Builder.PrivateNetworking))

	body, err := digitalocean.NewRequest(*d.Client, "droplets/new", params)
	if err != nil {
		return nil, err
	}

	info := &DropletInfo{}
	if err := mapstructure.Decode(body, info); err != nil {
		return nil, err
	}

	return info, nil
}

// Droplets returns a slice of all Droplets.
func (d *DigitalOcean) Droplets() ([]Droplet, error) {
	resp, err := digitalocean.NewRequest(*d.Client, "droplets", url.Values{})
	if err != nil {
		return nil, err
	}

	var result DropletsResp
	if err := mapstructure.Decode(resp, &result); err != nil {
		return nil, err
	}

	return result.Droplets, nil
}

// Image returns a single image based on the given snaphot name, slug or id. It
// checks for each occurency and returns for the first match.
func (d *DigitalOcean) Image(slug_or_name_or_id string) (digitalocean.Image, error) {
	return d.Client.Image(slug_or_name_or_id)
}

// MyImages returns a slice of all personal images.
func (d *DigitalOcean) MyImages() ([]digitalocean.Image, error) {
	v := url.Values{}
	v.Set("filter", "my_images")

	resp, err := digitalocean.NewRequest(*d.Client, "images", v)
	if err != nil {
		return nil, err
	}

	var result digitalocean.ImagesResp
	if err := mapstructure.Decode(resp, &result); err != nil {
		return nil, err
	}

	return result.Images, nil
}

// Start starts the machine for the given dropletID
func (d *DigitalOcean) Start(opts *protocol.MachineOptions) error {
	push := d.pusher(opts, machinestate.Starting)
	dropletId, err := d.DropletId()
	if err != nil {
		return err
	}

	push("Starting machine", 10)

	path := fmt.Sprintf("droplets/%v/power_on", dropletId)
	body, err := digitalocean.NewRequest(*d.Client, path, url.Values{})
	if err != nil {
		return err
	}

	push("Start message is being sent, waiting.", 30)

	eventId, ok := body["event_id"].(float64)
	if !ok {
		return fmt.Errorf("restart malformed data %v", body)
	}

	return d.WaitUntilReady(int(eventId), 30, 80, push)
}

// Stop stops the machine for the given dropletID
func (d *DigitalOcean) Stop(opts *protocol.MachineOptions) error {
	push := d.pusher(opts, machinestate.Stopping)
	dropletId, err := d.DropletId()
	if err != nil {
		return err
	}

	push("Stopping machine", 10)

	path := fmt.Sprintf("droplets/%v/shutdown", dropletId)
	body, err := digitalocean.NewRequest(*d.Client, path, url.Values{})
	if err != nil {
		return err
	}

	push("Stop message is being sent, waiting.", 30)

	eventId, ok := body["event_id"].(float64)
	if !ok {
		return fmt.Errorf("restart malformed data %v", body)
	}

	return d.WaitUntilReady(int(eventId), 30, 80, push)
}

// Restart restart the machine for the given dropletID
func (d *DigitalOcean) Restart(opts *protocol.MachineOptions) error {
	push := d.pusher(opts, machinestate.Rebooting)
	dropletId, err := d.DropletId()
	if err != nil {
		return err
	}

	push("Rebooting machine", 10)

	path := fmt.Sprintf("droplets/%v/reboot", dropletId)
	body, err := digitalocean.NewRequest(*d.Client, path, url.Values{})
	if err != nil {
		return err
	}

	push("Reboot message is being sent, waiting.", 30)

	eventId, ok := body["event_id"].(float64)
	if !ok {
		return fmt.Errorf("restart malformed data %v", body)
	}

	return d.WaitUntilReady(int(eventId), 30, 80, push)
}

// Destroyimage destroys an image for the given imageID.
func (d *DigitalOcean) DestroyImage(imageId uint) error {
	return d.Client.DestroyImage(imageId)
}

func (d *DigitalOcean) Regions() ([]digitalocean.Region, error) {
	return d.Client.Regions()
}

func (d *DigitalOcean) DestroyDroplet(dropletId uint) error {
	path := fmt.Sprintf("droplets/%v/destroy", dropletId)
	_, err := digitalocean.NewRequest(*d.Client, path, url.Values{})
	return err
}

// Destroy destroys the machine with the given droplet ID.
func (d *DigitalOcean) Destroy(opts *protocol.MachineOptions) error {
	push := d.pusher(opts, machinestate.Terminating)
	dropletId, err := d.DropletId()
	if err != nil {
		return err
	}

	push("Terminating machine", 10)

	path := fmt.Sprintf("droplets/%v/destroy", dropletId)
	body, err := digitalocean.NewRequest(*d.Client, path, url.Values{})
	if err != nil {
		return err
	}

	push("Terminating message is being sent, waiting.", 30)

	eventId, ok := body["event_id"].(float64)
	if !ok {
		return fmt.Errorf("restart malformed data %v", body)
	}

	return d.WaitUntilReady(int(eventId), 50, 80, push)
}

// CreateSnapshot cretes a new snapshot with the name from the given droplet Id.
func (d *DigitalOcean) CreateSnapshot(dropletId uint, name string) error {
	return d.Client.CreateSnapshot(dropletId, name)
}

// Info returns all information about the given droplet info.
func (d *DigitalOcean) Info(opts *protocol.MachineOptions) (interface{}, error) {
	dropletId, err := d.DropletId()
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("droplets/%v", dropletId)
	resp, err := digitalocean.NewRequest(*d.Client, path, url.Values{})
	if err != nil {
		return nil, err
	}

	droplet, ok := resp["droplet"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("malformed data received %v", resp)
	}

	var result Droplet
	if err := mapstructure.Decode(droplet, &result); err != nil {
		return nil, err
	}

	return result, err
}

func (d *DigitalOcean) DropletId(raws ...interface{}) (uint, error) {
	if d.Builder.DropletId == "" {
		return 0, errors.New("dropletId is not available")
	}

	dropletId := utils.ToUint(d.Builder.DropletId)
	if dropletId == 0 {
		return 0, fmt.Errorf("malformed data received %v. droplet Id must be an int.", d.Builder.DropletId)
	}

	return dropletId, nil
}
