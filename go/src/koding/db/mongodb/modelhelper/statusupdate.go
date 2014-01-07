package modelhelper

import (
	"koding/db/models"
	"koding/db/mongodb"
	"labix.org/v2/mgo"
)

var (
	POST_COLL = "jNewStatusUpdates"
)

func GetStatusUpdateById(id string) (*models.StatusUpdate, error) {
	statusUpdate := new(models.StatusUpdate)

	return statusUpdate, mongodb.One(POST_COLL, id, statusUpdate)
}

func UpdateStatusUpdate(s *models.StatusUpdate) error {
	query := func(c *mgo.Collection) error {
		return c.UpdateId(s.Id, s)
	}

	return mongodb.Run(POST_COLL, query)
}

func DeleteStatusUpdateById(id string) error {
	selector := Selector{"_id": GetObjectId(id)}
	query := func(c *mgo.Collection) error {
		return c.Remove(selector)
	}

	return mongodb.Run(POST_COLL, query)
}

func AddStatusUpdate(s *models.StatusUpdate) error {
	query := insertQuery(s)

	return mongodb.Run(POST_COLL, query)
}
