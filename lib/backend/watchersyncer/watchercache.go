// Copyright (c) 2017-2019 Tigera, Inc. All rights reserved.

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package watchersyncer

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/unai-ttxu/libcalico-go/lib/backend/api"
	"github.com/unai-ttxu/libcalico-go/lib/backend/model"
	cerrors "github.com/unai-ttxu/libcalico-go/lib/errors"
)

// The watcherCache provides watcher/syncer support for a single key type in the
// backend.  These results are sent to the main WatcherSyncer on a buffered "results"
// channel.  To ensure the order of events is received correctly by the main WatcherSyncer,
// we send all notification types in this channel.  Note that because of this the results
// channel is untyped - however the watcherSyncer only expects one of the following
// types:
// -  An error
// -  An api.Update
// -  A api.SyncStatus (only for the very first InSync notification)
type watcherCache struct {
	logger               *logrus.Entry
	client               api.Client
	watch                api.WatchInterface
	resources            map[string]cacheEntry
	oldResources         map[string]cacheEntry
	results              chan<- interface{}
	hasSynced            bool
	errors               int
	resourceType         ResourceType
	currentWatchRevision string
}

var (
	ListRetryInterval = 1000 * time.Millisecond
	WatchPollInterval = 5000 * time.Millisecond
	ErrorThreshold    = 15
)

// cacheEntry is an entry in our cache.  It groups the a key with the last known
// revision that we processed.  We store the revision so that we can determine
// if an entry has been updated (and therefore whether we need to send an update
// event in the syncer callback).
type cacheEntry struct {
	revision string
	key      model.Key
}

// Create a new watcherCache.
func newWatcherCache(client api.Client, resourceType ResourceType, results chan<- interface{}) *watcherCache {
	return &watcherCache{
		logger:       logrus.WithField("ListRoot", model.ListOptionsToDefaultPathRoot(resourceType.ListInterface)),
		client:       client,
		resourceType: resourceType,
		results:      results,
		resources:    make(map[string]cacheEntry, 0),
	}
}

// run creates the watcher and loops indefinitely reading from the watcher.
func (wc *watcherCache) run(ctx context.Context) {
	wc.logger.Debug("Watcher cache starting, start initial sync processing")
	wc.resyncAndCreateWatcher(ctx)

	wc.logger.Debug("Starting main event processing loop")
mainLoop:
	for {
		if wc.watch == nil {
			// The watcher will be nil if the context cancelled during a resync.
			wc.logger.Debug("Watch is nil. Returning")
			break mainLoop
		}
		select {
		case <-ctx.Done():
			wc.logger.Debug("Context is done. Returning")
			wc.cleanExistingWatcher()
			break mainLoop
		case event, ok := <-wc.watch.ResultChan():
			if !ok {
				// If the channel is closed then resync/recreate the watch.
				wc.logger.Info("Watch channel closed by remote - recreate watcher")
				wc.resyncAndCreateWatcher(ctx)
				continue
			}
			wc.logger.WithField("RC", wc.watch.ResultChan()).Debug("Reading event from results channel")

			// Handle the specific event type.
			switch event.Type {
			case api.WatchAdded, api.WatchModified:
				kvp := event.New
				wc.handleWatchListEvent(kvp)
			case api.WatchDeleted:
				// Nil out the value to indicate a delete.
				kvp := event.Old
				if kvp == nil {
					// Bug, we're about to panic when we hit the nil pointer, log something useful.
					wc.logger.WithField("watcher", wc).WithField("event", event).Panic("Deletion event without old value")
				}
				kvp.Value = nil
				wc.handleWatchListEvent(kvp)
			case api.WatchError:
				// Handle a WatchError.  First determine if the error type indicates that the
				// watch has closed, and if so we'll need to resync and create a new watcher.
				wc.results <- event.Error

				if e, ok := event.Error.(cerrors.ErrorWatchTerminated); ok {
					wc.logger.Debug("Received watch terminated error - recreate watcher")
					if !e.ClosedByRemote {
						// If the watcher was not closed by remote, reset the currentWatchRevision.  This will
						// trigger a full resync rather than simply trying to watch from the last event
						// revision.
						wc.logger.Debug("Watch was not closed by remote - full resync required")
						wc.currentWatchRevision = ""
						wc.onError()
					}
					wc.resyncAndCreateWatcher(ctx)
				} else {
					wc.onError()
				}

				if wc.errors > ErrorThreshold {
					// Trigger a full resync if we're past the error threshold.
					wc.currentWatchRevision = ""
					wc.resyncAndCreateWatcher(ctx)
				}
			default:
				// Unknown event type - not much we can do other than log.
				wc.logger.WithField("EventType", event.Type).Errorf("Unknown event type received from the datastore")
			}
		}
	}

	// The watcher cache has exited. This can only mean that it has been shutdown, so emit all updates in the cache as
	// delete events.
	for _, value := range wc.resources {
		wc.results <- []api.Update{{
			UpdateType: api.UpdateTypeKVDeleted,
			KVPair: model.KVPair{
				Key: value.key,
			},
		}}
	}
}

// resyncAndCreateWatcher loops performing resync processing until it successfully
// completes a resync and starts a watcher.
func (wc *watcherCache) resyncAndCreateWatcher(ctx context.Context) {
	// The passed in context allows a resync to be stopped mid-resync. The resync should be stopped as quickly as
	// possible, but there should be usable data available in wc.resources so that delete events can be sent.
	// The strategy is to
	// - cancel any long running functions calls made from here, i.e. pass ctx to the client.list() calls
	//    - but if it finishes, then ensure that the listing gets processed.
	// - cancel any sleeps if the context is cancelled

	// Make sure any previous watcher is stopped.
	wc.logger.Debug("Starting watch sync/resync processing")
	wc.cleanExistingWatcher()

	// If we don't have a currentWatchRevision then we need to perform a full resync.
	performFullResync := wc.currentWatchRevision == ""

	for {
		select {
		case <-ctx.Done():
			wc.logger.Debug("Context is done. Returning")
			wc.cleanExistingWatcher()
			return
		default:
			// Start the resync.  This processing loops until we create the watcher.  If the
			// watcher continuously fails then this loop effectively becomes a polling based
			// syncer.
			wc.logger.Debug("Starting main resync loop")
		}

		if performFullResync {
			wc.logger.Debug("Full resync is required")

			// Notify the converter that we are resyncing.
			if wc.resourceType.UpdateProcessor != nil {
				wc.logger.Debug("Trigger converter resync notification")
				wc.resourceType.UpdateProcessor.OnSyncerStarting()
			}

			// Start the sync by Listing the current resources.
			l, err := wc.client.List(ctx, wc.resourceType.ListInterface, "")
			if err != nil {
				// Failed to perform the list.  Pause briefly (so we don't tight loop) and retry.
				wc.logger.WithError(err).Info("Failed to perform list of current data during resync")
				wc.onError()
				select {
				case <-time.After(ListRetryInterval):
					continue
				case <-ctx.Done():
					wc.logger.Debug("Context is done. Returning")
					wc.cleanExistingWatcher()
					return
				}
			}

			// Once this point is reached, it's important not to drop out if the context is cancelled.
			// Move the current resources over to the oldResources
			wc.oldResources = wc.resources
			wc.resources = make(map[string]cacheEntry, 0)

			// Send updates for each of the resources we listed - this will revalidate entries in
			// the oldResources map.
			for _, kvp := range l.KVPairs {
				wc.handleWatchListEvent(kvp)
			}

			// We've listed the current settings.  Complete the sync by notifying the main WatcherSyncer
			// go routine (if we haven't already) and by sending deletes for the old resources that were
			// not acknowledged by the List.  The oldResources will be empty after this call.
			wc.finishResync()

			// Store the current watch revision.  This gets updated on any new add/modified event.
			wc.currentWatchRevision = l.Revision
		}

		// And now start watching from the revision returned by the List, or from a previous watch event
		// (depending on whether we were performing a full resync).
		w, err := wc.client.Watch(ctx, wc.resourceType.ListInterface, wc.currentWatchRevision)
		if err != nil {
			// Failed to create the watcher - we'll need to retry.
			if _, ok := err.(cerrors.ErrorOperationNotSupported); ok {
				// Watch is not supported on this resource type, so pause for the watch poll interval.
				// This loop effectively becomes a poll loop for this resource type.
				wc.logger.Debug("Watch operation not supported")
				select {
				case <-time.After(WatchPollInterval):
					continue
				case <-ctx.Done():
					wc.logger.Debug("Context is done. Returning")
					wc.cleanExistingWatcher()
					return
				}
			}

			// We hit an error creating the Watch.  Trigger a full resync.
			// TODO We should be able to improve this by handling specific error cases with another
			//      watch retry.  This would require some care to ensure the correct errors are captured
			//      for the different datastore drivers.
			wc.logger.WithError(err).WithField("performFullResync", performFullResync).Info("Failed to create watcher")
			wc.onError()
			performFullResync = true
			continue
		}

		// Store the watcher and exit back to the main event loop.
		wc.logger.Debug("Resync completed, now watching for change events")
		wc.watch = w
		return
	}
}

func (wc *watcherCache) cleanExistingWatcher() {
	if wc.watch != nil {
		wc.logger.Debug("Stopping previous watcher")
		wc.watch.Stop()
		wc.watch = nil
	}
}

// finishResync handles processing to finish synchronization.
// If this watcher has never been synced then notify the main watcherSyncer that we've synced.
// We may also need to send deleted messages for old resources that were not validated in the
// resync (i.e. they must have since been deleted).
func (wc *watcherCache) finishResync() {
	// If we haven't already sent an InSync event (or signalled the inverse by sending a WaitForDatastore event),
	// then send a synced notification.  The watcherSyncer will send a Synced event when it has received synced
	// events from each cache.
	if !wc.hasSynced {
		wc.logger.Info("Sending synced update")
		wc.results <- api.InSync
		wc.hasSynced = true
		wc.errors = 0
	}

	// If the watcher failed at any time, we end up recreating a watcher and storing off
	// the current known resources for revalidation.  Now that we have finished the sync,
	// any of the remaining resources that were not accounted for must have been deleted
	// and we need to send deleted events for them.
	numOldResources := len(wc.oldResources)
	if numOldResources > 0 {
		wc.logger.WithField("Num", numOldResources).Debug("Sending resync deletes")
		updates := make([]api.Update, 0, len(wc.oldResources))
		for _, r := range wc.oldResources {
			updates = append(updates, api.Update{
				UpdateType: api.UpdateTypeKVDeleted,
				KVPair: model.KVPair{
					Key: r.key,
				},
			})
		}
		wc.results <- updates
	}
	wc.oldResources = nil
}

// handleWatchListEvent handles a watch event converting it if required and passing to
// handleConvertedWatchEvent to send the appropriate update types.
func (wc *watcherCache) handleWatchListEvent(kvp *model.KVPair) {
	// Track the resource version from this watch/list event.
	wc.currentWatchRevision = kvp.Revision

	if wc.resourceType.UpdateProcessor == nil {
		// No update processor - handle immediately.
		wc.handleConvertedWatchEvent(kvp)
		wc.errors = 0
		return
	}

	// We have an update processor so use that to convert the event data.
	kvps, err := wc.resourceType.UpdateProcessor.Process(kvp)
	for _, kvp := range kvps {
		wc.handleConvertedWatchEvent(kvp)
	}

	// If we hit a conversion error, notify the main syncer.
	if err != nil {
		wc.results <- err
	} else {
		wc.errors = 0
	}
}

// handleConvertedWatchEvent handles a converted watch event fanning out
// to the add/mod or delete processing as necessary.
func (wc *watcherCache) handleConvertedWatchEvent(kvp *model.KVPair) {
	if kvp.Value == nil {
		wc.handleDeletedUpdate(kvp.Key)
	} else {
		wc.handleAddedOrModifiedUpdate(kvp)
	}
}

// handleAddedOrModifiedUpdate handles a single Added or Modified update request.
// Whether we send an Added or Modified depends on whether we have already sent
// an added notification for this resource.
func (wc *watcherCache) handleAddedOrModifiedUpdate(kvp *model.KVPair) {
	thisKey := kvp.Key
	thisKeyString := thisKey.String()
	thisRevision := kvp.Revision
	wc.markAsValid(thisKeyString)

	// If the resource is already in our map, then this is a modified event.  Check the
	// revision to see if we actually need to send an update.
	if resource, ok := wc.resources[thisKeyString]; ok {
		if resource.revision == thisRevision {
			// No update to revision, so no event to send.
			wc.logger.WithField("Key", thisKeyString).Debug("Swallowing event update from datastore because entry is same as cached entry")
			return
		}
		// Resource is modified, send an update event and store the latest revision.
		wc.logger.WithField("Key", thisKeyString).Debug("Datastore entry modified, sending syncer update")
		wc.results <- []api.Update{{
			UpdateType: api.UpdateTypeKVUpdated,
			KVPair:     *kvp,
		}}
		resource.revision = thisRevision
		wc.resources[thisKeyString] = resource
		return
	}

	// The resource has not been seen before, so send a new event, and store the
	// current revision.
	wc.logger.WithField("Key", thisKeyString).Debug("Cache entry added, sending syncer update")
	wc.results <- []api.Update{{
		UpdateType: api.UpdateTypeKVNew,
		KVPair:     *kvp,
	}}
	wc.resources[thisKeyString] = cacheEntry{
		revision: thisRevision,
		key:      thisKey,
	}
}

// handleDeletedWatchEvent sends a deleted event and removes the resource key from our cache.
func (wc *watcherCache) handleDeletedUpdate(key model.Key) {
	thisKeyString := key.String()
	wc.markAsValid(thisKeyString)

	// If we have seen an added event for this key then send a deleted event and remove
	// from the cache.
	if _, ok := wc.resources[thisKeyString]; ok {
		wc.logger.WithField("Key", thisKeyString).Debug("Datastore entry deleted, sending syncer update")
		wc.results <- []api.Update{{
			UpdateType: api.UpdateTypeKVDeleted,
			KVPair: model.KVPair{
				Key: key,
			},
		}}
		delete(wc.resources, thisKeyString)
	}
}

// markAsValid marks a resource that we have just seen as valid, by moving it from the set of
// "oldResources" that were stored during the resync back into the main "resources" set.  Any entries
// remaining in the oldResources map once the current snapshot events have been processed, indicates
// entries that were deleted during the resync - see corresponding code in finishResync().
func (wc *watcherCache) markAsValid(resourceKey string) {
	if wc.oldResources != nil {
		if oldResource, ok := wc.oldResources[resourceKey]; ok {
			wc.logger.WithField("Key", resourceKey).Debug("Marking key as re-processed")
			wc.resources[resourceKey] = oldResource
			delete(wc.oldResources, resourceKey)
		}
	}
}

// onError signals to the syncer that this watcherCache is not in-sync if the number of consecutive errors
// exceeds the error threshold.  See finishResync() for how the watcherCache goes back to in-sync.
func (wc *watcherCache) onError() {
	wc.errors++
	if wc.hasSynced && wc.errors > ErrorThreshold {
		wc.logger.WithFields(logrus.Fields{"errors": wc.errors, "threshold": ErrorThreshold}).Debugf("Exceeded error threshold")
		wc.hasSynced = false
		wc.results <- api.WaitForDatastore
	}
}
