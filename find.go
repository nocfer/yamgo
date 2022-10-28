package yamgo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type PopulateOptions struct {
	On         string
	Path       string
	Projection []string
}

func (mf *yamgo) FindOne(filter bson.M, b interface{}) (err error) {

	ctx, cancel := context.WithTimeout(context.Background(), MediumTimeout*time.Second)

	defer cancel()

	res := mf.col.FindOne(ctx, filter)

	if res.Err() != nil {
		return res.Err()
	}

	err = res.Decode(b)

	if err != nil {
		return err
	}

	return nil
}

func (mf *yamgo) FindByID(id string, result interface{}) (err error) {
	objectID, err := primitive.ObjectIDFromHex(id)

	if err != nil {
		return err
	}

	return mf.FindOne(bson.M{"_id": objectID}, result)
}

func (mf *yamgo) FindByObjectID(objectID primitive.ObjectID, result interface{}) (err error) {

	if err != nil {
		return err
	}

	return mf.FindOne(bson.M{"_id": objectID}, result)
}

func (mf *yamgo) Find(filter bson.M, results interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), LongTimeout*time.Second)
	defer cancel()

	cur, err := mf.col.Find(ctx, filter)
	if err != nil {
		return err
	}
	err = cur.All(ctx, results)
	if err != nil {
		return err
	}
	return nil
}

func (mf *yamgo) executeCursorQuery(query []bson.M, sort bson.D, limit int64, collation *options.Collation, hint interface{}, projection string, results interface{}) error {

	options := options.Find()
	options.SetSort(sort)
	options.SetLimit(limit + 1)

	ctx, cancel := context.WithTimeout(context.Background(), LongTimeout*time.Second)
	defer cancel()

	if collation != nil {
		options.SetCollation(collation)
	}

	if hint != nil {
		options.SetHint(hint)
	}

	if projection != "" {
		pMap := make(map[string]bool)
		str := strings.ReplaceAll(projection, "id", "_id")
		for _, key := range strings.Split(str, ",") {
			pMap[key] = true
		}
		options.SetProjection(pMap)
	}

	cursor, err := mf.col.Find(ctx, bson.M{"$and": query}, options)
	if err != nil {
		return err
	}
	err = cursor.All(ctx, results)

	if err != nil {
		return err
	}

	return nil
}

func (mf *yamgo) PaginatedFind(params PaginationFindParams, results interface{}) (Page, error) {

	var err error

	if results == nil {
		return Page{}, errors.New("results can't be nil")
	}

	params = ensureMandatoryParams(params)
	shouldSecondarySortOnID := params.PaginatedField != "_id"

	// Compute total count of documents matching filter - only computed if CountTotal is True
	var count int
	if params.CountTotal {
		count, err = mf.CountDocuments([]bson.M{params.Query})
		if err != nil {
			return Page{}, err
		}
	}

	queries, sort, err := BuildQueries(params)

	if err != nil {
		return Page{}, err
	}

	// Execute the augmented query, get an additional element to see if there's another page
	err = mf.executeCursorQuery(queries, sort, params.Limit, params.Collation, params.Hint, params.Projection, results)

	if err != nil {
		return Page{}, err
	}

	// Get the results slice's pointer and value
	resultsPtr := reflect.ValueOf(results)
	resultsVal := resultsPtr.Elem()

	hasMore := resultsVal.Len() > int(params.Limit)

	// Remove the extra element that we added to see if there was another page
	if hasMore {
		resultsVal = resultsVal.Slice(0, resultsVal.Len()-1)
	}

	hasPrevious := params.Next != "" || (params.Previous != "" && hasMore)
	hasNext := params.Previous != "" || hasMore

	var previousCursor string
	var nextCursor string

	if resultsVal.Len() > 0 {
		// If we sorted reverse to get the previous page, correct the sort order
		if params.Previous != "" {
			for left, right := 0, resultsVal.Len()-1; left < right; left, right = left+1, right-1 {
				leftValue := resultsVal.Index(left).Interface()
				resultsVal.Index(left).Set(resultsVal.Index(right))
				resultsVal.Index(right).Set(reflect.ValueOf(leftValue))
			}
		}

		// Generate the previous cursor
		if hasPrevious {
			firstResult := resultsVal.Index(0).Interface()
			previousCursor, err = generateCursor(firstResult, params.PaginatedField, shouldSecondarySortOnID)
			if err != nil {
				return Page{}, fmt.Errorf("could not create a previous cursor: %s", err)
			}
		}

		// Generate the next cursor
		if hasNext {
			lastResult := resultsVal.Index(resultsVal.Len() - 1).Interface()
			nextCursor, err = generateCursor(lastResult, params.PaginatedField, shouldSecondarySortOnID)
			if err != nil {
				return Page{}, fmt.Errorf("could not create a next cursor: %s", err)
			}
		}
	}

	// Create the response cursor
	page := Page{
		Previous:    previousCursor,
		HasPrevious: hasPrevious,
		Next:        nextCursor,
		HasNext:     hasNext,
		Count:       count,
	}

	// Save the modified result slice in the result pointer
	resultsPtr.Elem().Set(resultsVal)

	return page, nil
}

func (mf *yamgo) FindWithOptions(filter bson.M, option options.FindOptions, results interface{}) error {

	ctx, cancel := context.WithTimeout(context.Background(), LongTimeout*time.Second)

	defer cancel()

	cur, err := mf.col.Find(ctx, filter, &option)
	if err != nil {
		return err
	}
	err = cur.All(ctx, results)
	if err != nil {
		return err
	}
	return nil
}

func (mf *yamgo) FindOneAndPopulate(filter bson.M, findOptions options.FindOptions, populate []PopulateOptions, result interface{}) error {
	findOptions.SetLimit(-1)
	return mf.FindAndPopulate(filter, findOptions, populate, result)
}

func (mf *yamgo) FindAndPopulate(filter bson.M, option options.FindOptions, populate []PopulateOptions, results interface{}) error {

	ctx, cancel := context.WithTimeout(context.Background(), LongTimeout*time.Second)

	defer cancel()

	matchStage := bson.D{
		{Key: "$match", Value: filter},
	}

	pipeline := mongo.Pipeline{}
	pipeline = append(pipeline, matchStage)

	for _, value := range populate {
		pipeline = append(pipeline, buildLookupStage(value), buildAddFieldStage(value))
	}

	cur, err := mf.col.Aggregate(ctx, pipeline)

	if err != nil {
		return err
	}

	if *option.Limit < 0 {
		if cur.Next(ctx) {

			if err := cur.Decode(results); err != nil {
				return err
			}
			fmt.Println(results)
		}

	} else {
		err = cur.All(ctx, results)
	}

	if err != nil {
		return err
	}

	return nil
}

func buildAddFieldStage(populate PopulateOptions) bson.D {
	return bson.D{{Key: "$addFields", Value: bson.D{{Key: populate.Path, Value: bson.D{{Key: "$first", Value: "$" + populate.Path}}}}}}
}

func buildLookupStage(populate PopulateOptions) bson.D {
	projectionStage := bson.D{}
	for _, projectionField := range populate.Projection {
		projectionStage = append(projectionStage, bson.E{Key: projectionField, Value: 1})
	}

	return bson.D{
		{Key: "$lookup",
			Value: bson.D{
				{Key: "from", Value: populate.On},
				{Key: "let", Value: bson.D{{Key: "oId", Value: "$" + populate.Path}}},
				{Key: "pipeline",
					Value: bson.A{
						bson.D{
							{Key: "$match",
								Value: bson.D{
									{Key: "$expr",
										Value: bson.D{
											{Key: "$eq",
												Value: bson.A{
													"$_id",
													"$$oId",
												},
											},
										},
									},
								},
							},
						},
						bson.D{
							{Key: "$project", Value: projectionStage},
						},
					},
				},
				{Key: "as", Value: populate.Path},
			},
		},
	}
}