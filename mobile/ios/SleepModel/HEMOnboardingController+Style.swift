//
//  HEMOnboardingController+Style.swift
//  Sense
//
//  Created by Jimmy Lu on 3/16/17.
//  Copyright © 2017 Hello. All rights reserved.
//

import Foundation

extension HEMOnboardingController {
    
    @objc override func applyStyle() {
        let aClass = HEMOnboardingController.self
        let bgColor = SenseStyle.color(aClass: aClass, property: .backgroundColor)
        self.view.backgroundColor = bgColor
        self.titleLabel?.font = SenseStyle.font(aClass: aClass, property: .textFont)
        self.titleLabel?.textColor = SenseStyle.color(aClass: aClass, property: .textColor)
        self.titleLabel?.layer.borderWidth = 0.0;
        self.descriptionLabel?.textColor = SenseStyle.color(aClass: aClass, property: .detailColor)
        self.descriptionLabel?.font = SenseStyle.font(aClass: aClass, property: .detailFont)
    }
    
    @objc func titleAttributes() -> [NSAttributedString.Key : Any] {
        let aClass = HEMOnboardingController.self
        return [NSAttributedString.Key.font : SenseStyle.font(aClass: aClass, property: .textFont),
                NSAttributedString.Key.foregroundColor : SenseStyle.color(aClass: aClass, property: .textColor)]
    }
    
    @objc func descriptionAttributes() -> [NSAttributedString.Key : Any] {
        let aClass = HEMOnboardingController.self
        return [NSAttributedString.Key.font : SenseStyle.font(aClass: aClass, property: .detailFont),
                NSAttributedString.Key.foregroundColor : SenseStyle.color(aClass: aClass, property: .detailColor),
                NSAttributedString.Key.paragraphStyle : NSMutableParagraphStyle.senseStyle()]
    }
    
}
