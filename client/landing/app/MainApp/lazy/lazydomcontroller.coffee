class LazyDomController extends KDController

  constructor:->
    super

    @groupViewsAdded   = no
    @profileViewsAdded = no

    @mainController = @getSingleton 'mainController'

    @mainController.on 'AppIsReady', =>
      if @userEnteredFromGroup()
        @addGroupViews()
      else if @userEnteredFromProfile()
        @addProfileViews()

      if KD.isLoggedIn() and KD.config.groupEntryPoint is 'koding'
        @hideLandingPage()
      else
        landingPageSideBar = new LandingPageSideBar

  hideLandingPage:->

    if $('#group-landing').length
      $('#group-landing').css 'opacity', 0
      @utils.wait 600, -> $('#group-landing').hide()

    else if $('#profile-landing').length
      $('#profile-landing').css 'opacity', 0
      @utils.wait 600, -> $('#profile-landing').hide()

  userEnteredFromGroup:-> KD.config.groupEntryPoint?

  userEnteredFromProfile:-> KD.config.profileEntryPoint?

  addGroupViews:->

    return if @groupViewsAdded
    @groupViewsAdded = yes

    groupLandingView = new KDView
      lazyDomId : 'group-landing'

    groupLandingView.listenWindowResize()
    groupLandingView._windowDidResize = =>
      groupLandingView.setHeight window.innerHeight
      groupContentView.setHeight window.innerHeight-groupTitleView.getHeight()

    groupContentWrapperView = new KDView
      lazyDomId : 'group-content-wrapper'
      cssClass : 'slideable'

    groupTitleView = new KDView
      lazyDomId : 'group-title'

    groupContentView = new KDView
      lazyDomId : 'group-loading-content'

    groupPersonalWrapperView = new KDView
      lazyDomId : 'group-personal-wrapper'
      cssClass  : 'slideable'
      click :(event)=>
        unless event.target.tagName is 'A'
          @mainController.loginScreen.unsetClass 'landed'

    groupLogoView = new KDView
      lazyDomId: 'group-koding-logo'
      click :=>
        groupPersonalWrapperView.setClass 'slide-down'
        groupContentWrapperView.setClass 'slide-down'
        groupLogoView.setClass 'top'

        groupLandingView.setClass 'group-fading'
        @utils.wait 1100, => groupLandingView.setClass 'group-hidden'

    groupLogoView.setY groupLandingView.getHeight()-42

    @utils.wait =>
      groupLogoView.setClass 'animate'
      groupLandingView._windowDidResize()


  addProfileViews:->

    return if @profileViewsAdded
    @profileViewsAdded = yes

    new StaticProfileController
