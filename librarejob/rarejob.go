package librarejob

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tebeka/selenium"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NOTE: Cookie analysis
//  rarejob_auto_login == rarejob_onetime_key
//  PHPSESSID and PHPSESSID_HIGH are session ids
//  once rarejob_onetime_key and PHPSESSID are deleted, session is closed and we're redirected to login page.

type Reserve struct {
	Name string
	StartAt time.Time
	EndAt time.Time
}

type Tutor struct {
	Name string
	AvailableSlots []time.Time
}

func (t Tutor) MarshalLogObject(enc zapcore.ObjectEncoder) error {
    enc.AddString("name", t.Name)
	// TODO(musaprg): output availableslots
    return nil
}

type Tutors []Tutor

func (ts Tutors) MarshalLogArray(enc zapcore.ArrayEncoder) error {
    for _, t := range ts {
        enc.AppendObject(t)
    }
    return nil
}

type Client interface {
	Login(ctx context.Context, username, password string) error
	ReserveTutor(ctx context.Context, from time.Time, by time.Duration) (*Reserve, error)
	Teardown() error
}

type client struct {
	s *selenium.Service
	wd selenium.WebDriver
}

func NewClient() (Client, error) {
	// Start a Selenium WebDriver server instance (if one is not already
	// running).
	const (
		// These paths will be different on your system.
		seleniumPath    = "/opt/selenium/selenium-server-standalone.jar"
		geckoDriverPath = "/usr/bin/geckodriver"
		port            = 8080
	)
	opts := []selenium.ServiceOption{
		selenium.StartFrameBuffer(),           // Start an X frame buffer for the browser to run in.
		selenium.GeckoDriver(geckoDriverPath), // Specify the path to GeckoDriver in order to use Firefox.
	}
	selenium.SetDebug(false)
	service, err := selenium.NewSeleniumService(seleniumPath, port, opts...)
	if err != nil {
		return nil, err
	}

	// Connect to the WebDriver instance running locally.
	caps := selenium.Capabilities{"browserName": "firefox"}
	wd, err := selenium.NewRemote(caps, fmt.Sprintf("http://localhost:%d/wd/hub", port))
	if err != nil {
		return nil, err
	}

	return &client{
		s: service,
		wd: wd,
	}, nil
}

func (c *client) Login(ctx context.Context, username, password string) error {
	// TODO(musaprg): Cache SESSIONID and reuse
	if err := c.wd.Get(rarejobLoginURL); err != nil {
		return fmt.Errorf("failed to access rarejob login page: %w", err)
	}

	_ = waitUntilElementLoaded(c.wd, selenium.ByCSSSelector, loginPageEmailSelector)

	if emailInput, err := c.wd.FindElement(selenium.ByCSSSelector, loginPageEmailSelector); err != nil {
		return fmt.Errorf("failed to find the email input box: %w", err)
	} else {
		emailInput.SendKeys(os.Getenv("RAREJOB_EMAIL"))
	}

	_ = waitUntilElementLoaded(c.wd, selenium.ByCSSSelector, loginPagePasswordSelector)

	if passwordInput, err := c.wd.FindElement(selenium.ByCSSSelector, loginPagePasswordSelector); err != nil {
		return fmt.Errorf("failed to find the password input box: %w", err)
	} else {
		passwordInput.SendKeys(os.Getenv("RAREJOB_PASSWORD"))
	}

	if submit, err := c.wd.FindElement(selenium.ByName, "yt0"); err != nil {
		return fmt.Errorf("failed to find submit button: %w", err)
	} else {
		submit.Click()
	}

	if err := c.wd.Wait(func(wd selenium.WebDriver) (bool, error) {
		return wd.SessionID() != "", nil
	}); err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	return nil
}

func (c *client) ReserveTutor(ctx context.Context, from time.Time, margin time.Duration) (*Reserve, error) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	// TODO(musaprg): split this function into two

	// -- Search available tutors --

	by := from.Local().Add(margin)
	if !(margin < 24 * time.Hour && from.Hour() < by.Hour()) {
		return nil, ErrSpreadAcrossTwoDays
	}

	queryURL, err := generateTutorSearchQuery(from, by)
	if err != nil {
		return nil, fmt.Errorf("failed to generate search query: %w", err)
	}
	if err := c.wd.Get(queryURL); err != nil {
		return nil, fmt.Errorf("failed to get availabe tutor list: %w", err)
	}

	waitUntilElementLoaded(c.wd, selenium.ByCSSSelector, tutorListSelector)
	tutorList, err := c.wd.FindElements(selenium.ByCSSSelector, tutorListSelector)
	if err != nil {
		return nil, fmt.Errorf("failed to get tutor info: %w", err)
	}

	var tutors Tutors
	// TODO(musaprg): parallelize with goroutine and use errgroup to aggregate error
	for tnum := 1; tnum <= len(tutorList); tnum++ {
		logger.Debug("getting tutor info", zap.Int("number", tnum))
		nameElm, _ := c.wd.FindElement(selenium.ByCSSSelector, fmt.Sprintf(tutorNameSelector, tnum))
		name, _ := nameElm.Text()
		slotElms, err := c.wd.FindElements(selenium.ByCSSSelector, fmt.Sprintf(tutorTimeSlotSelector, tnum))
		if err != nil {
			return nil, fmt.Errorf("failed to get time slots for tutor #%d: %w", tnum, err)
		}
		var slots []time.Time
		for snum := 1; snum <= len(slotElms); snum++ {
			slotElm, err := c.wd.FindElement(selenium.ByCSSSelector, fmt.Sprintf(tutorTimeSlotButtonSelector, tnum, snum))
			if err != nil { // if err, fill zero time to preserve index
				slots = append(slots, time.Time{})
			}
			slotText, _ := slotElm.Text()
			h,m,err := parseTime(slotText)
			if err != nil {
				slots = append(slots, time.Time{})
			}
			slots = append(slots, time.Date(from.Year(), from.Month(), from.Day(), h, m, 0, 0, time.Local))
		}
		tutors = append(tutors, Tutor{
			Name: name,
			AvailableSlots: slots,
		})
	}

	logger.Info("found tutors", zap.Array("tutors", tutors))

	// -- Do reservation --

	timeSlotButtonSelector := fmt.Sprintf(tutorTimeSlotButtonSelector, 1, 1)
	waitUntilElementLoaded(c.wd, selenium.ByCSSSelector, timeSlotButtonSelector)
	// TODO(musaprg): Implement to select tutor, not hard-coded
	timeSlot, err := c.wd.FindElement(selenium.ByCSSSelector, timeSlotButtonSelector)
	if err != nil {
		return nil, fmt.Errorf("failed to find time slot button: %w", err)
	}
	timeSlot.Click()

	if url, _ := c.wd.CurrentURL(); url != "" {
		logger.Debug("current url:", zap.String("url", url))
	}

	waitUntilElementLoaded(c.wd, selenium.ByLinkText, "予約する")
	reserveButton, err := c.wd.FindElement(selenium.ByLinkText, "予約する")
	if err != nil {
		return nil, fmt.Errorf("failed to get reserve button: %w", err)
	}
	reserveButton.Click()

	waitUntilURLChanged(c.wd, rarejobReservationFinishURL)

	return &Reserve{
		Name: tutors[0].Name,
		StartAt: tutors[0].AvailableSlots[0],
		EndAt: tutors[0].AvailableSlots[0].Add(25 * time.Minute),
	}, nil
}

func (c *client) Teardown() error {
	if err := c.wd.Quit(); err != nil {
		return fmt.Errorf("failed to quit current webdriver session: %w", err)
	}
	if err := c.s.Stop(); err != nil {
		return fmt.Errorf("failed to quit current webdriver session: %w", err)
	}
	return nil
}