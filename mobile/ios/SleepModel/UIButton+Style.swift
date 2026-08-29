//
//  UIButton+Style.swift
//  Sense
//
//  Created by Jimmy Lu on 3/14/17.
//  Copyright © 2017 Hello. All rights reserved.
//

import Foundation

extension UIButton {
    
    @objc func applyStyle() {
        let aClass = UIButton.self
        self.backgroundColor = SenseStyle.color(aClass: aClass, property: .backgroundColor)
        self.titleLabel?.font = SenseStyle.font(aClass: aClass, property: .primaryButtonTextFont)
        self.setTitleColor(SenseStyle.color(aClass: aClass, property: .primaryButtonTextColor), for: UIControl.State.normal)
        self.setTitleColor(SenseStyle.color(aClass: aClass, property: .primaryButtonTextDisabledColor), for: UIControl.State.disabled)
        self.setTitleColor(SenseStyle.color(aClass: aClass, property: .primaryButtonTextHighlightedColor), for: UIControl.State.highlighted)
    }
    
    @objc func applySecondaryStyle() {
        let aClass = UIButton.self
        self.backgroundColor = SenseStyle.color(aClass: aClass, property: .backgroundColor)
        self.titleLabel?.font = SenseStyle.font(aClass: aClass, property: .secondaryButtonTextFont)
        self.setTitleColor(SenseStyle.color(aClass: aClass, property: .secondaryButtonTextColor), for: UIControl.State.normal)
        self.setTitleColor(SenseStyle.color(aClass: aClass, property: .secondaryButtonTextDisabledColor), for: UIControl.State.disabled)
        self.setTitleColor(SenseStyle.color(aClass: aClass, property: .secondaryButtonTextHighlightedColor), for: UIControl.State.highlighted)
    }
    
}
