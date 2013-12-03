Bongo          = require "bongo"
{Relationship} = require "jraphical"
_              = require "underscore"

JAccount       = require "./account"
JTag           = require "./tag"

{secure, daisy, Base} = Bongo

module.exports = class ActiveItems extends Base
  @share()

  @set
    sharedMethods :
      static      : ["fetchTopics", "fetchUsers"]

  nameMapping =
    user    :
      klass : JAccount
      as    : ["creator", "follower"]
    topic   :
      klass : JTag
      as    : ["developer", "follower", "post"]

  @fetchUsers = secure (client, options = {}, callback) ->
    @fetch "user", options, callback

  @fetchTopics = secure (client, options = {}, callback) ->
    @fetch "topic", options, callback

  @fetch = (name, options = {}, callback) ->
    mapping     = nameMapping[name]
    {klass, as} = mapping

    greater = (new Date(Date.now() - 1000*60*60*24))

    matcher     = {
      sourceName : klass.name
      as         : $in  : as
      timestamp  : $gte : greater
    }

    Relationship.getCollection().aggregate {$match: matcher},
      {$group:{_id:"$sourceId", total:{$sum:1}}},
      {$limit:10},
    , (err, items)->
      return callback err  if err

      items = _.sortBy items, (item)-> item.sum
      items = items.reverse()
      items = items[0..10]

      instances = []
      daisy queue = items.map (item) ->
        ->
          klass.one _id: item._id, (err, instance)->
            instances.push instance
            queue.next()

      queue.push -> callback null, instances
