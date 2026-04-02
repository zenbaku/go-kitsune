// Package kmongo provides MongoDB source and sink helpers for kitsune
// pipelines.
//
// Users own the [mongo.Client] and collections — configure connection strings,
// auth, and pool settings yourself. Kitsune will never create or close clients.
//
// Stream query results:
//
//	client, _ := mongo.Connect(options.Client().ApplyURI(uri))
//	defer client.Disconnect(ctx)
//
//	coll := client.Database("mydb").Collection("events")
//	pipe := kmongo.Find(coll, bson.M{"status": "pending"}, func(cur *mongo.Cursor) (Event, error) {
//	    var e Event
//	    return e, cur.Decode(&e)
//	})
//	pipe.ForEach(handle).Run(ctx)
//
// Tail a change stream:
//
//	pipe := kmongo.Watch(coll, mongo.Pipeline{}, func(cs *mongo.ChangeStream) (ChangeEvent, error) {
//	    var e ChangeEvent
//	    return e, cs.Decode(&e)
//	})
//
// Bulk insert:
//
//	kitsune.Batch(pipe, 100).
//	    ForEach(kmongo.InsertMany(coll, func(e Event) (any, error) { return e, nil })).
//	    Run(ctx)
package kmongo

import (
	"context"

	kitsune "github.com/zenbaku/go-kitsune"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Find creates a Pipeline that streams results from a MongoDB query. decode is
// called once per document to convert the cursor into a value of type T.
//
// The collection is not closed when the pipeline ends — the caller owns it.
func Find[T any](coll *mongo.Collection, filter any, decode func(*mongo.Cursor) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		cur, err := coll.Find(ctx, filter)
		if err != nil {
			return err
		}
		defer cur.Close(ctx) //nolint:errcheck

		for cur.Next(ctx) {
			v, err := decode(cur)
			if err != nil {
				return err
			}
			if !yield(v) {
				return nil
			}
		}
		return cur.Err()
	})
}

// Watch creates a Pipeline that streams MongoDB change stream events. decode is
// called once per event to convert the change stream into a value of type T.
//
// The pipeline runs until the context is cancelled or an error occurs.
// The collection is not closed when the pipeline ends — the caller owns it.
func Watch[T any](coll *mongo.Collection, pipeline any, decode func(*mongo.ChangeStream) (T, error)) *kitsune.Pipeline[T] {
	return kitsune.Generate(func(ctx context.Context, yield func(T) bool) error {
		cs, err := coll.Watch(ctx, pipeline)
		if err != nil {
			return err
		}
		defer cs.Close(ctx) //nolint:errcheck

		for cs.Next(ctx) {
			v, err := decode(cs)
			if err != nil {
				return err
			}
			if !yield(v) {
				return nil
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		return cs.Err()
	})
}

// InsertMany returns a batch sink function that bulk-inserts items into a
// MongoDB collection. marshal converts each item into a document to insert.
// Use with [kitsune.Pipeline.ForEach] after [kitsune.Batch].
//
// The collection is not closed when the pipeline ends — the caller owns it.
func InsertMany[T any](coll *mongo.Collection, marshal func(T) (any, error)) func(context.Context, []T) error {
	return func(ctx context.Context, items []T) error {
		docs := make([]any, len(items))
		for i, item := range items {
			doc, err := marshal(item)
			if err != nil {
				return err
			}
			docs[i] = doc
		}
		_, err := coll.InsertMany(ctx, docs)
		return err
	}
}
