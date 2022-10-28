package yamgo

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

func (mf *yamgo) CountDocuments(filter []bson.M) (int, error) {

	ctx, cancel := context.WithTimeout(context.Background(), LongTimeout*time.Second)
	defer cancel()

	count, err := mf.col.CountDocuments(ctx, bson.M{"$and": filter})
	if err != nil {
		return 0, err
	}
	return int(count), nil
}