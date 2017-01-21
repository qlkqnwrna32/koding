uuid          = require 'uuid'
async         = require 'async'
jraphical     = require 'jraphical'
KodingError   = require '../error'
{ permit }    = require './group/permissionset'
{ secure
  ObjectId
  signature } = require 'bongo'


module.exports = class JApiToken extends jraphical.Module

  JGroup     = require './group'
  JAccount   = require './account'
  Validators = require './group/validators'

  @API_TOKEN_LIMIT = 5

  PERMISSION_EDIT_GROUPS = [
    { permission: 'edit groups',     superadmin: yes }
    { permission: 'edit own groups', validateWith: Validators.group.admin }
  ]

  @share()

  @set
    sharedEvents       :
      static           : []
      instance         : []
    indexes            :
      code             : 'unique'
    sharedMethods      :
      static           :
        create         :
          (signature Function)
      instance         :
        remove         :
          (signature Function)
    schema             :
      code             :
        type           : String
        required       : yes
        default        : uuid.v4
      group            :
        type           : String
        required       : yes
      originId         :
        type           : ObjectId
        required       : yes
      createdAt        :
        type           : Date
        default        : -> new Date


  @fetchGroup = (group, callback) ->

    JGroup.one { slug: group }, (err, group) ->
      return callback err  if err
      return callback new KodingError 'No such team!'  unless group

      if not !!group.getAt 'isApiEnabled'
        callback new KodingError 'API usage is not enabled for this team.'
      else
        callback null, group


  @create = (data, callback) ->

    { account, group } = data

    token    = null
    groupObj = null

    unless account and group
      return callback new KodingError 'account and group slug must be set!'

    queue = [

      (next) ->
        # validating data params
        unless account instanceof JAccount
          return next new KodingError 'account is not an instance of Jaccount!'
        JApiToken.fetchGroup group, next

      (next) ->
        limitError = "You can't have more than #{JApiToken.API_TOKEN_LIMIT} API tokens"
        JApiToken.count { group }, (err, count) ->
          return next err  if err
          if count >= JApiToken.API_TOKEN_LIMIT
            return next new KodingError limitError
          next()

      (next) ->
        # creating token
        token = new JApiToken
          group    : group
          originId : account.getId()

        token.save (err) ->
          return next err  if err
          token.username = account.profile.nickname
          next()

    ]

    async.series queue, (err) ->
      return callback err  if err
      callback null, token


  @create$ = permit
    advanced: PERMISSION_EDIT_GROUPS
    success: (client, callback) ->

      group    = client?.context?.group
      account  = client?.connection?.delegate

      unless account and group
        return callback new KodingError 'account and group must be set!'

      data = { group, account }
      JApiToken.create data, callback


  remove$: permit
    advanced: PERMISSION_EDIT_GROUPS
    success: (client, callback) ->
      @remove callback
