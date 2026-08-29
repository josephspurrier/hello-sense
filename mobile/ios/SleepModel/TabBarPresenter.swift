//
//  TabBarPresenter.swift
//  Sense
//
//  Created by Jimmy Lu on 1/18/17.
//  Copyright © 2017 Hello. All rights reserved.
//

import Foundation

class TabBarPresenter: HEMPresenter {
    
    fileprivate static let itemInset = CGFloat(6)
    fileprivate weak var tabBar: UITabBar?
    
    func bind(with tabBar: UITabBar!) {
        self.tabBar = tabBar
        self.adjustInsets()
    }
    
    func adjustInsets() {
        guard let tabBar = self.tabBar else {
            return
        }

        // UITabBar.appearance() in Theme only styles bars created after it is
        // set, so this already-live bar kept whatever it launched with: it
        // stayed transparent and showed each screen's background through, and a
        // theme change never reached it. Style the instance directly. This runs
        // again on every theme reload because MainViewController calls back
        // into adjustInsets when the tabs are rebuilt.
        let barTint = SenseStyle.color(aClass: UITabBar.self, property: .barTintColor)
        let appearance = UITabBarAppearance()
        appearance.configureWithOpaqueBackground()
        appearance.backgroundColor = barTint
        tabBar.standardAppearance = appearance
        tabBar.scrollEdgeAppearance = appearance
        // hide titles and center tab icons from the controllers
        let aClass = UITabBar.self
        let color = SenseStyle.color(aClass: aClass, property: .titleColor)
        let font = SenseStyle.font(aClass: aClass, property: .titleFont)
        let topInset = TabBarPresenter.itemInset
        let inset = UIEdgeInsets(top: TabBarPresenter.itemInset, left: 0, bottom: -topInset, right: 0)
        for item in tabBar.items! {
            let titleAttributes: [NSAttributedString.Key: Any] = [NSAttributedString.Key.foregroundColor : color,
                                                  NSAttributedString.Key.font : font]
            item.setTitleTextAttributes(titleAttributes, for: UIControl.State.normal)
            item.setTitleTextAttributes(titleAttributes, for: UIControl.State.selected)
            item.imageInsets = inset
            item.title = nil
        }
    }
}
