//
//  HEMActionButton+Style.swift
//  Sense
//
//  Created by Jimmy Lu on 3/13/17.
//  Copyright © 2017 Hello. All rights reserved.
//

import Foundation

extension HEMActionButton {
    
    @objc override func applyStyle() {
        super.applyStyle()
        let aClass = HEMActionButton.self
        self.setBackgroundColor(SenseStyle.color(aClass: aClass, property: .backgroundColor), for: UIControl.State.normal)
        self.setBackgroundColor(SenseStyle.color(aClass: aClass, property: .backgroundDisabledColor), for: UIControl.State.disabled)
        self.setBackgroundColor(SenseStyle.color(aClass: aClass, property: .backgroundHighlightedColor), for: UIControl.State.highlighted)
        self.titleLabel?.font = SenseStyle.font(aClass: aClass, property: .textFont)
        self.setTitleColor(SenseStyle.color(aClass: aClass, property: .textColor), for: UIControl.State.normal)
        self.setTitleColor(SenseStyle.color(aClass: aClass, property: .textDisabledColor), for: UIControl.State.disabled)
        self.setTitleColor(SenseStyle.color(aClass: aClass, property: .textHighlightedColor), for: UIControl.State.highlighted)
    }
    
}
